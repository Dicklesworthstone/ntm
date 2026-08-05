package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/recovery"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var (
	errGrokSpawnWaitUnavailable   = errors.New("--spawn-wait is not yet supported for Grok Build because its authenticated TUI readiness protocol has not been verified")
	errGrokSpawnAssignUnavailable = errors.New("--spawn-assign-work is not yet supported for Grok Build because prompt delivery has not been verified")
)

// readyWordRe matches "ready" as a whole word. A bare substring check also hit
// "already", so lines like "Already up to date." read as an agent coming up.
var readyWordRe = regexp.MustCompile(`\bready\b`)

// Pre-compiled prompt patterns for isAgentReady (anchored to end of lines or output).
var promptPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\$\s*$`), // Empty Shell prompt
	regexp.MustCompile(`(?m)^%\s*$`),  // Empty Zsh prompt
	regexp.MustCompile(`❯\s*$`),       // Modern prompts (U+276F)
	regexp.MustCompile(`›\s*$`),       // Codex prompt (U+203A) empty
	regexp.MustCompile(`(?m)^›`),      // Codex prompt with hint text
	regexp.MustCompile(`>\s*$`),       // Simple prompt at end of output
	regexp.MustCompile(`(?m)^>\s*$`),  // Simple prompt on its own line
}

// SpawnOptions configures the robot-spawn operation.
type SpawnOptions struct {
	Session            string
	Label              string        // Session label — constructs "{Session}--{Label}" if set
	ConfigPath         string        // Selected global config used for authoritative assignment policy
	RequireConfig      bool          // ConfigPath was explicitly selected and must exist
	CCCount            int           // Claude agents
	CodCount           int           // Codex agents
	GmiCount           int           // Gemini agents
	AgyCount           int           // Antigravity agents
	GrokCount          int           // Grok Build agents
	Preset             string        // Recipe/preset name
	NoUserPane         bool          // Don't create user pane
	WorkingDir         string        // Override working directory
	WaitReady          bool          // Wait for agents to be ready
	ReadyTimeout       time.Duration // Timeout for ready detection
	DryRun             bool          // Preview mode: show what would happen without executing
	Safety             bool          // Fail if session already exists
	AssignWork         bool          // Enable orchestrator work assignment mode
	AssignStrategy     string        // Assignment strategy: top-n, diverse, dependency-aware, skill-matched
	CustomNames        []string      // Custom agent names (used in order, then NATO alphabet)
	RequireReservation bool
	ReservationPaths   []string
	AssignmentDeps     *SpawnAssignmentDependencies
	LifecycleDeps      *SpawnLifecycleDependencies
}

// SpawnLifecycleDependencies exposes tmux lifecycle ports for deterministic
// terminal-contract tests. Production callers leave this nil.
type SpawnLifecycleDependencies struct {
	IsTMUXInstalled  func() bool
	GetAllPanes      func(context.Context) (map[string][]tmux.Pane, error)
	SessionExists    func(context.Context, string) (bool, error)
	CreateSession    func(context.Context, string, string, int) error
	GetPanes         func(context.Context, string) ([]tmux.Pane, error)
	SplitWindow      func(context.Context, string, string) (string, error)
	ApplyTiledLayout func(context.Context, string) error
	LaunchAgent      func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error)
	WaitForReady     func(context.Context, *SpawnOutput, time.Duration) error
}

// SpawnAssignmentDependencies exposes assignment side-effect ports for focused
// tests while production uses the durable Beads, ledger, and dispatch services.
type SpawnAssignmentDependencies struct {
	LoadAssignmentPolicy             func(string, string, bool) (*config.Config, error)
	FetchActionable                  func(context.Context, string, int) ([]bv.TriageRecommendation, error)
	FetchTriage                      func(context.Context, string) (*bv.TriageResponse, error)
	AssignmentLedgerExists           func(session string) (bool, error)
	ListPanes                        func(context.Context, string) ([]tmux.Pane, error)
	LoadStore                        func(session string) (*assignment.AssignmentStore, error)
	ClaimBead                        func(context.Context, string, string, string) (bv.BeadClaimResult, error)
	ClaimBeadWithOperatorGatedLabels func(context.Context, string, string, string, []string) (bv.BeadClaimResult, error)
	GetBeadStatus                    func(context.Context, string, string) (string, error)
	GetBeadDetails                   func(context.Context, string, string) (*bv.BeadAssignmentDetails, error)
	NewIdempotencyKey                func() (string, error)
	ReservationPort                  assignment.ReservationPort
	ResolveAgentName                 func(context.Context, string, string, string, string) (string, error)
	ObserveSession                   func(context.Context, string) (statuspkg.SessionObservation, error)
	DispatchDeliverer                dispatchsvc.Deliverer
	DispatchPacer                    dispatchsvc.Pacer
}

// SpawnOutput is the structured output for --robot-spawn.
type SpawnOutput struct {
	RobotResponse
	Session    string `json:"session"`
	CreatedAt  string `json:"created_at"`
	PresetUsed string `json:"preset_used,omitempty"`
	WorkingDir string `json:"working_dir"`
	// EffectiveProjectKey is the fully symlink-resolved working directory —
	// the key Agent Mail actually registers reservations and identities under.
	//
	// It is reported separately because the two can differ: reaching a project
	// through a symlinked alias (or macOS /Users vs /private/var/Users) makes
	// NTM see one path while Agent Mail canonicalizes to another, so an
	// orchestrator that trusts working_dir queries a project key that has none
	// of its own swarm's reservations in it. That divergence is silent, and
	// AGENTS.md calls project-key mismatch the single most common source of
	// cross-tool breakage. Emitting the resolved key makes it checkable
	// (ntm-cx4e). Omitted when it is identical to working_dir.
	EffectiveProjectKey string            `json:"effective_project_key,omitempty"`
	Agents              []SpawnedAgent    `json:"agents"`
	Layout              string            `json:"layout"`
	TotalStartupMs      int64             `json:"total_startup_ms"`
	Error               string            `json:"error,omitempty"`
	DryRun              bool              `json:"dry_run,omitempty"`
	WouldCreate         []SpawnedAgent    `json:"would_create,omitempty"`
	Mode                string            `json:"mode,omitempty"`
	Assignments         []SpawnAssignment `json:"assignments,omitempty"`
	AssignStrategy      string            `json:"assign_strategy,omitempty"`
	Recovery            *SpawnRecovery    `json:"recovery,omitempty"`
	// Admission is the pre-spawn resource-pressure admission result.
	Admission *pressure.SpawnAdmission `json:"admission,omitempty"`
}

func setSpawnCancellation(output *SpawnOutput, err error) {
	if output == nil || err == nil {
		return
	}
	output.Error = err.Error()
	output.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Retry the command after cancellation")
}

func setSpawnSafetyConflict(output *SpawnOutput, session string) {
	if output == nil {
		return
	}
	output.Error = fmt.Sprintf("session '%s' already exists (--spawn-safety mode prevents reuse; use 'ntm kill %s' first)", session, session)
	output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", output.Error), ErrCodeInvalidFlag, "Choose a new session name or disable --spawn-safety")
}

func spawnCancellationError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func newSpawnOutput(startTime time.Time, opts SpawnOptions) *SpawnOutput {
	return &SpawnOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CreatedAt:     startTime.UTC().Format(time.RFC3339),
		PresetUsed:    opts.Preset,
		Agents:        []SpawnedAgent{},
		Layout:        "tiled",
	}
}

func validateSpawnRequest(opts SpawnOptions) (string, error) {
	counts := []struct {
		flag  string
		value int
	}{
		{flag: "--spawn-cc", value: opts.CCCount},
		{flag: "--spawn-cod", value: opts.CodCount},
		{flag: "--spawn-gmi", value: opts.GmiCount},
		{flag: "--spawn-agy", value: opts.AgyCount},
		{flag: "--spawn-grok", value: opts.GrokCount},
	}
	for _, count := range counts {
		if count.value < 0 {
			return "", fmt.Errorf("%s must be zero or greater, got %d", count.flag, count.value)
		}
	}
	if opts.CCCount+opts.CodCount+opts.GmiCount+opts.AgyCount+opts.GrokCount <= 0 {
		return "", errors.New("no agents specified (use cc, cod, gmi, agy, or grok counts)")
	}
	if opts.GrokCount > 0 && opts.WaitReady {
		return "", errGrokSpawnWaitUnavailable
	}
	if opts.GrokCount > 0 && opts.AssignWork {
		return "", errGrokSpawnAssignUnavailable
	}
	if !opts.AssignWork {
		return "", nil
	}
	strategy, err := normalizeAssignStrategyStrict(opts.AssignStrategy)
	if err != nil {
		return "", err
	}
	return strategy, nil
}

func validateGrokSpawnPaneBaselines(panes []tmux.Pane, opts SpawnOptions) error {
	if opts.GrokCount <= 0 {
		return nil
	}
	start := opts.CCCount + opts.CodCount + opts.GmiCount + opts.AgyCount
	if !opts.NoUserPane {
		start++
	}
	for i := 0; i < opts.GrokCount; i++ {
		paneIndex := start + i
		if paneIndex >= len(panes) {
			return fmt.Errorf("requested Grok Build pane %d is unavailable", i+1)
		}
		if err := tmux.ValidatePaneLaunchBaseline(panes[paneIndex]); err != nil {
			return fmt.Errorf("validate Grok Build agent %d: %w", i+1, err)
		}
	}
	return nil
}

func validateExistingGrokSpawnPaneBaselines(panes []tmux.Pane, opts SpawnOptions) error {
	if opts.GrokCount <= 0 {
		return nil
	}
	start := opts.CCCount + opts.CodCount + opts.GmiCount + opts.AgyCount
	if !opts.NoUserPane {
		start++
	}
	for i := 0; i < opts.GrokCount; i++ {
		paneIndex := start + i
		if paneIndex >= len(panes) {
			break
		}
		if err := tmux.ValidatePaneLaunchBaseline(panes[paneIndex]); err != nil {
			return fmt.Errorf("validate Grok Build agent %d: %w", i+1, err)
		}
	}
	return nil
}

func spawnLifecycleDeps(custom *SpawnLifecycleDependencies) SpawnLifecycleDependencies {
	deps := SpawnLifecycleDependencies{
		IsTMUXInstalled:  tmux.IsInstalled,
		GetAllPanes:      tmux.GetAllPanesContext,
		SessionExists:    tmux.SessionExistsContext,
		CreateSession:    tmux.CreateSessionWithHistoryLimitContext,
		GetPanes:         tmux.GetPanesContext,
		SplitWindow:      tmux.SplitWindowContext,
		ApplyTiledLayout: tmux.ApplyTiledLayoutContext,
		LaunchAgent:      launchAgent,
		WaitForReady:     waitForAgentsReady,
	}
	if custom == nil {
		return deps
	}
	if custom.IsTMUXInstalled != nil {
		deps.IsTMUXInstalled = custom.IsTMUXInstalled
	}
	if custom.GetAllPanes != nil {
		deps.GetAllPanes = custom.GetAllPanes
	}
	if custom.SessionExists != nil {
		deps.SessionExists = custom.SessionExists
	}
	if custom.CreateSession != nil {
		deps.CreateSession = custom.CreateSession
	}
	if custom.GetPanes != nil {
		deps.GetPanes = custom.GetPanes
	}
	if custom.SplitWindow != nil {
		deps.SplitWindow = custom.SplitWindow
	}
	if custom.ApplyTiledLayout != nil {
		deps.ApplyTiledLayout = custom.ApplyTiledLayout
	}
	if custom.LaunchAgent != nil {
		deps.LaunchAgent = custom.LaunchAgent
	}
	if custom.WaitForReady != nil {
		deps.WaitForReady = custom.WaitForReady
	}
	return deps
}

// SpawnRecovery contains session recovery context loaded from handoff.
type SpawnRecovery struct {
	HandoffPath  string `json:"handoff_path,omitempty"`  // Path to handoff file
	HandoffAge   string `json:"handoff_age,omitempty"`   // Human-readable age
	Goal         string `json:"goal,omitempty"`          // What previous session achieved
	Now          string `json:"now,omitempty"`           // What this session should do
	Status       string `json:"status,omitempty"`        // Previous session status
	Outcome      string `json:"outcome,omitempty"`       // Previous session outcome
	InjectedText string `json:"injected_text,omitempty"` // Formatted text injected into agents
}

// SpawnAssignment represents a work assignment to a spawned agent.
type SpawnAssignment struct {
	Pane              string `json:"pane"`        // Pane reference (e.g., "0.1")
	AgentType         string `json:"agent_type"`  // claude, codex, gemini
	BeadID            string `json:"bead_id"`     // Assigned bead ID
	BeadTitle         string `json:"bead_title"`  // Bead title for context
	Priority          string `json:"priority"`    // Bead priority (P0-P4)
	Claimed           bool   `json:"claimed"`     // Whether bead was successfully claimed (marked in_progress)
	PromptSent        bool   `json:"prompt_sent"` // Whether the work prompt was sent to the agent
	ClaimActor        string `json:"claim_actor,omitempty"`
	IdempotencyKey    string `json:"idempotency_key,omitempty"`
	DispatchReceiptID string `json:"dispatch_receipt_id,omitempty"`
	ReservationIDs    []int  `json:"reservation_ids,omitempty"`
	ClaimError        string `json:"claim_error,omitempty"`  // Error during claim, if any
	PromptError       string `json:"prompt_error,omitempty"` // Error sending prompt, if any
}

// SpawnedAgent represents an agent created during spawn.
type SpawnedAgent struct {
	Pane      string `json:"pane"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type"`
	Variant   string `json:"variant,omitempty"`
	Title     string `json:"title"`
	Ready     bool   `json:"ready"`
	StartupMs int64  `json:"startup_ms"`
	Error     string `json:"error,omitempty"`
}

func collectSpawnAdmissionInput(ctx context.Context, opts SpawnOptions, cfg *config.Config, totalAgents, totalPanes int) pressure.SpawnAdmissionInput {
	return collectSpawnAdmissionInputWithPanes(ctx, opts, cfg, totalAgents, totalPanes, tmux.GetAllPanesContext)
}

func collectSpawnAdmissionInputWithPanes(
	ctx context.Context,
	opts SpawnOptions,
	cfg *config.Config,
	totalAgents, totalPanes int,
	getAllPanes func(context.Context) (map[string][]tmux.Pane, error),
) pressure.SpawnAdmissionInput {
	input := pressure.SpawnAdmissionInput{
		Session:         opts.Session,
		RequestedAgents: totalAgents,
		RequestedPanes:  totalPanes,
	}

	if cfg == nil || cfg.SpawnPacing.Enabled {
		input.LargeSpawnThreshold = pressure.DefaultBudget().MaxPipelineFanout
		if cfg != nil {
			if cfg.SpawnPacing.MaxConcurrentSpawns > 0 {
				input.LargeSpawnThreshold = cfg.SpawnPacing.MaxConcurrentSpawns
			}
			input.MaxAgents = spawnAdmissionAgentLimit(cfg)
		}
		input.Pressure = collectSystemPressureSnapshot(ctx)
	}

	panesBySession, err := getAllPanes(ctx)
	if err != nil {
		return input
	}
	input.RunningSessions = len(panesBySession)
	for session, panes := range panesBySession {
		if session == opts.Session {
			input.SessionPanes = len(panes)
		}
		input.CurrentPanes += len(panes)
		for _, pane := range panes {
			if isSpawnAdmissionAgentPane(pane) {
				input.RunningAgents++
			}
		}
	}
	return input
}

func spawnAdmissionAgentLimit(cfg *config.Config) int {
	if cfg == nil {
		return 0
	}
	caps := cfg.SpawnPacing.AgentCaps
	total := 0
	for _, cap := range []int{caps.ClaudeMaxConcurrent, caps.CodexMaxConcurrent, caps.GeminiMaxConcurrent} {
		if cap > 0 {
			total += cap
		}
	}
	return total
}

func collectSystemPressureSnapshot(ctx context.Context) pressure.Snapshot {
	pressureCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	g := pressure.New(pressure.Config{
		Mode:      pressure.ModeEnforce,
		Providers: []pressure.Provider{pressure.NewSystemProvider()},
	})
	return g.Refresh(pressureCtx)
}

func isSpawnAdmissionAgentPane(pane tmux.Pane) bool {
	agentType := pane.Type.Canonical()
	return agentType != "" && agentType != tmux.AgentUser
}

// GetSpawn creates a session with agents and returns structured output.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSpawn(ctx context.Context, opts SpawnOptions, cfg *config.Config) (*SpawnOutput, error) {
	if ctx == nil {
		return nil, errors.New("robot spawn context is required")
	}
	startTime := time.Now()
	output := newSpawnOutput(startTime, opts)
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	auditWorkingDir := ""
	auditSessionCreated := false
	auditPanesAdded := 0

	// Validate project name unconditionally: "--" is reserved for labels.
	if err := config.ValidateProjectName(opts.Session); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Project names cannot contain '--' (reserved as label separator)")
		output.Error = err.Error()
		return output, nil
	}

	// Apply goal label to session name (bd-1933u)
	if opts.Label != "" {
		if err := config.ValidateLabel(opts.Label); err != nil {
			labelErr := fmt.Errorf("invalid label: %w", err)
			output.RobotResponse = NewErrorResponse(labelErr, ErrCodeInvalidFlag, "Use a valid label (alphanumeric, dash, underscore)")
			output.Error = labelErr.Error()
			return output, nil
		}
		opts.Session = config.FormatSessionName(opts.Session, opts.Label)
		output.Session = opts.Session
	}

	assignStrategy, validationErr := validateSpawnRequest(opts)
	if validationErr != nil {
		output.Error = validationErr.Error()
		errorCode := ErrCodeInvalidFlag
		hint := "Use non-negative agent counts and a supported assignment strategy"
		if errors.Is(validationErr, errGrokSpawnWaitUnavailable) || errors.Is(validationErr, errGrokSpawnAssignUnavailable) {
			errorCode = ErrCodeNotImplemented
			hint = agentpkg.GrokPhaseOneCapabilityHint
		}
		output.RobotResponse = NewErrorResponse(validationErr, errorCode, hint)
		return output, nil
	}
	deps := spawnLifecycleDeps(opts.LifecycleDeps)
	if err := ctx.Err(); err != nil {
		setSpawnCancellation(output, err)
		return output, nil
	}
	_ = audit.LogEvent(opts.Session, audit.EventTypeSpawn, audit.ActorSystem, "robot.spawn", map[string]interface{}{
		"phase":           "start",
		"session":         opts.Session,
		"total_agents":    opts.CCCount + opts.CodCount + opts.GmiCount + opts.AgyCount + opts.GrokCount,
		"preset":          opts.Preset,
		"no_user_pane":    opts.NoUserPane,
		"dry_run":         opts.DryRun,
		"safety":          opts.Safety,
		"assign_work":     opts.AssignWork,
		"assign_strategy": opts.AssignStrategy,
		"correlation_id":  correlationID,
	}, nil)
	defer func() {
		agentsLaunched := 0
		if output != nil {
			agentsLaunched = len(output.Agents)
		}
		success := output != nil && output.Success
		payload := map[string]interface{}{
			"phase":           "finish",
			"session":         opts.Session,
			"total_agents":    opts.CCCount + opts.CodCount + opts.GmiCount + opts.AgyCount + opts.GrokCount,
			"preset":          opts.Preset,
			"no_user_pane":    opts.NoUserPane,
			"dry_run":         opts.DryRun,
			"safety":          opts.Safety,
			"assign_work":     opts.AssignWork,
			"assign_strategy": opts.AssignStrategy,
			"session_created": auditSessionCreated,
			"panes_added":     auditPanesAdded,
			"agents_launched": agentsLaunched,
			"success":         success,
			"duration_ms":     time.Since(auditStart).Milliseconds(),
			"working_dir":     auditWorkingDir,
			"correlation_id":  correlationID,
		}
		if output != nil && output.Error != "" {
			payload["error"] = output.Error
		}
		_ = audit.LogEvent(opts.Session, audit.EventTypeSpawn, audit.ActorSystem, "robot.spawn", payload, nil)
	}()

	// Validate session name
	if err := tmux.ValidateSessionName(opts.Session); err != nil {
		output.Error = fmt.Sprintf("invalid session name: %v", err)
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use a valid tmux session name")
		return output, nil
	}

	// Check tmux availability
	if !deps.IsTMUXInstalled() {
		output.Error = "tmux is not installed"
		output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", output.Error), ErrCodeDependencyMissing, "Install tmux to spawn sessions")
		return output, nil
	}

	// Safety check: fail if session already exists (when --spawn-safety is enabled)
	if opts.Safety {
		exists, err := deps.SessionExists(ctx, opts.Session)
		if err != nil {
			if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
				setSpawnCancellation(output, cancelErr)
			} else {
				output.Error = fmt.Sprintf("checking session: %v", err)
				output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux availability")
			}
			return output, nil
		}
		if exists {
			setSpawnSafetyConflict(output, opts.Session)
			return output, nil
		}
	}

	// Get working directory
	dir := opts.WorkingDir
	if dir == "" && cfg != nil {
		dir = cfg.GetProjectDir(opts.Session)
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			output.Error = fmt.Sprintf("could not determine working directory: %v", err)
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check working directory permissions")
			return output, nil
		}
	}
	output.WorkingDir = dir
	// Surface the key Agent Mail will actually register under whenever it
	// differs from the path NTM was given, so the divergence is checkable
	// instead of silent (ntm-cx4e).
	if canonical := agentmail.CanonicalProjectKey(dir); canonical != dir {
		output.EffectiveProjectKey = canonical
	}
	auditWorkingDir = dir

	var verifiedAssignmentPlan *bv.TriageResponse
	if opts.AssignWork {
		assignmentDeps := spawnAssignmentDeps(opts.AssignmentDeps)
		effectiveConfig, policyErr := assignmentDeps.LoadAssignmentPolicy(dir, opts.ConfigPath, opts.RequireConfig)
		if policyErr != nil {
			output.Error = fmt.Sprintf("load spawn assignment safety policy: %v", policyErr)
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("%s", output.Error),
				ErrCodeInvalidFlag,
				"Fix the selected global config and the spawn project's .ntm/config.toml",
			)
			return output, nil
		}
		cfg = effectiveConfig
		operatorGatedLabels := []string(nil)
		if effectiveConfig != nil {
			operatorGatedLabels = effectiveConfig.Assign.OperatorGatedLabels
		}
		if policyErr := bv.ConfigureProjectOperatorGatedLabels(dir, operatorGatedLabels); policyErr != nil {
			output.Error = fmt.Sprintf("register spawn assignment safety policy: %v", policyErr)
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("%s", output.Error),
				ErrCodeInvalidFlag,
				"Use an authoritative project directory for spawn assignment",
			)
			return output, nil
		}

		actionable, actionableErr := assignmentDeps.FetchActionable(ctx, dir, 0)
		if actionableErr != nil {
			if cancelErr := spawnCancellationError(ctx, actionableErr); cancelErr != nil {
				setSpawnCancellation(output, cancelErr)
			} else {
				output.Error = fmt.Sprintf("verify actionable spawn work: %v", actionableErr)
				output.RobotResponse = NewErrorResponse(
					fmt.Errorf("%s", output.Error),
					ErrCodeInternalError,
					"Ensure bv plan output and live br labels are complete and valid",
				)
			}
			return output, nil
		}
		actionable = filterAssignableActionableRecommendationsForProject(dir, actionable, 0)
		verifiedAssignmentPlan = restrictTriageToAssignable(nil, actionable)
	}

	// Load handoff context for session recovery (non-fatal if not found)
	spawnRecovery, handoffCtx := loadLatestHandoff(dir, opts.Session)
	if spawnRecovery != nil {
		output.Recovery = spawnRecovery
	}
	// handoffCtx is available for use in work prompts below
	_ = handoffCtx // silence unused warning when not in orchestrator mode

	totalAgents := opts.CCCount + opts.CodCount + opts.GmiCount + opts.AgyCount + opts.GrokCount

	// Calculate total panes needed
	totalPanes := totalAgents
	if !opts.NoUserPane {
		totalPanes++
	}

	var admissionTopologyErr error
	admissionInput := collectSpawnAdmissionInputWithPanes(
		ctx, opts, cfg, totalAgents, totalPanes,
		func(callCtx context.Context) (map[string][]tmux.Pane, error) {
			panesBySession, err := deps.GetAllPanes(callCtx)
			admissionTopologyErr = err
			return panesBySession, err
		},
	)
	if cancelErr := spawnCancellationError(ctx, admissionTopologyErr); cancelErr != nil {
		setSpawnCancellation(output, cancelErr)
		return output, nil
	}
	admission := pressure.EvaluateSpawnAdmission(admissionInput)
	output.Admission = &admission
	if err := ctx.Err(); err != nil {
		setSpawnCancellation(output, err)
		return output, nil
	}
	if !opts.DryRun && admission.Decision != pressure.SpawnAdmissionAdmit {
		output.Error = fmt.Sprintf("spawn admission %s: %s", admission.Decision, admission.Reason)
		hint := admission.Hint
		if hint == "" {
			hint = "Reduce requested agents or wait for resource headroom"
		}
		output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", output.Error), ErrCodeResourceBusy, hint)
		return output, nil
	}

	// Dry-run mode: show what would happen without executing
	if opts.DryRun {
		output.DryRun = true
		output.WouldCreate = []SpawnedAgent{}

		// Initialize name map for dry-run preview
		var dryRunNameMap *AgentNameMap
		if len(opts.CustomNames) > 0 {
			dryRunNameMap = NewAgentNameMapWithCustomNames(opts.Session, opts.CustomNames)
		} else {
			dryRunNameMap = NewAgentNameMap(opts.Session)
		}

		// Build list of what would be created
		paneIdx := 0
		if !opts.NoUserPane {
			userPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  userPane,
				Name:  dryRunNameMap.AssignNew("user", userPane),
				Type:  "user",
				Title: fmt.Sprintf("%s__user", opts.Session),
				Ready: true,
			})
			paneIdx++
		}

		for i := 0; i < opts.CCCount; i++ {
			ccPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  ccPane,
				Name:  dryRunNameMap.AssignNew("claude", ccPane),
				Type:  "claude",
				Title: fmt.Sprintf("%s__cc_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		for i := 0; i < opts.CodCount; i++ {
			codPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  codPane,
				Name:  dryRunNameMap.AssignNew("codex", codPane),
				Type:  "codex",
				Title: fmt.Sprintf("%s__cod_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		for i := 0; i < opts.GmiCount; i++ {
			gmiPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  gmiPane,
				Name:  dryRunNameMap.AssignNew("gemini", gmiPane),
				Type:  "gemini",
				Title: fmt.Sprintf("%s__gmi_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		for i := 0; i < opts.AgyCount; i++ {
			agyPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  agyPane,
				Name:  dryRunNameMap.AssignNew("antigravity", agyPane),
				Type:  "antigravity",
				Title: fmt.Sprintf("%s__agy_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		for i := 0; i < opts.GrokCount; i++ {
			grokPane := fmt.Sprintf("0.%d", paneIdx)
			output.WouldCreate = append(output.WouldCreate, SpawnedAgent{
				Pane:  grokPane,
				Name:  dryRunNameMap.AssignNew("grok", grokPane),
				Type:  "grok",
				Title: fmt.Sprintf("%s__grok_%d", opts.Session, i+1),
			})
			paneIdx++
		}

		output.Layout = "tiled"
		return output, nil
	}
	if err := ctx.Err(); err != nil {
		setSpawnCancellation(output, err)
		return output, nil
	}

	// Recheck immediately before lifecycle mutation. In safety mode a session
	// may have appeared while assignment and admission preflights were running.
	sessionCreated := false
	sessionExists, sessionErr := deps.SessionExists(ctx, opts.Session)
	if sessionErr != nil {
		if cancelErr := spawnCancellationError(ctx, sessionErr); cancelErr != nil {
			setSpawnCancellation(output, cancelErr)
		} else {
			output.Error = fmt.Sprintf("checking session: %v", sessionErr)
			output.RobotResponse = NewErrorResponse(sessionErr, ErrCodeInternalError, "Check tmux availability")
		}
		return output, nil
	}
	if opts.Safety && sessionExists {
		setSpawnSafetyConflict(output, opts.Session)
		return output, nil
	}

	// Ensure directory exists (only for real spawns, not dry-run).
	if err := os.MkdirAll(dir, 0755); err != nil {
		output.Error = fmt.Sprintf("creating directory: %v", err)
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check directory permissions")
		return output, nil
	}

	// Create session if it doesn't exist.
	if !sessionExists {
		if err := ctx.Err(); err != nil {
			setSpawnCancellation(output, err)
			return output, nil
		}
		historyLimit := tmux.DefaultHistoryLimit
		if cfg != nil && cfg.Tmux.HistoryLimit > 0 {
			historyLimit = cfg.Tmux.HistoryLimit
		}
		if err := deps.CreateSession(ctx, opts.Session, dir, historyLimit); err != nil {
			if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
				setSpawnCancellation(output, cancelErr)
			} else {
				output.Error = fmt.Sprintf("creating session: %v", err)
				output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux availability and session name")
			}
			return output, nil
		}
		sessionCreated = true
		auditSessionCreated = true
	}

	// Get current panes
	panes, err := deps.GetPanes(ctx, opts.Session)
	if err != nil {
		if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
			setSpawnCancellation(output, cancelErr)
		} else {
			output.Error = fmt.Sprintf("getting panes: %v", err)
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		}
		return output, nil
	}
	if err := validateExistingGrokSpawnPaneBaselines(panes, opts); err != nil {
		output.Error = fmt.Sprintf("validating existing Grok Build launch panes: %v", err)
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Use an idle shell pane before launching Grok Build",
		)
		return output, nil
	}

	// Add more panes if needed
	existingPanes := len(panes)
	if existingPanes < totalPanes {
		toAdd := totalPanes - existingPanes
		auditPanesAdded = toAdd
		for i := 0; i < toAdd; i++ {
			if err := ctx.Err(); err != nil {
				setSpawnCancellation(output, err)
				return output, nil
			}
			if _, err := deps.SplitWindow(ctx, opts.Session, dir); err != nil {
				if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
					setSpawnCancellation(output, cancelErr)
				} else {
					output.Error = fmt.Sprintf("creating pane: %v", err)
					output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux pane layout constraints")
				}
				return output, nil
			}
		}
	}

	// Get updated pane list
	panes, err = deps.GetPanes(ctx, opts.Session)
	if err != nil {
		if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
			setSpawnCancellation(output, cancelErr)
		} else {
			output.Error = fmt.Sprintf("getting panes: %v", err)
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		}
		return output, nil
	}
	if len(panes) < totalPanes {
		output.Error = fmt.Sprintf(
			"spawn topology has %d pane(s), but %d are required for %d agent(s)",
			len(panes), totalPanes, totalAgents,
		)
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("%s", output.Error),
			ErrCodePaneNotFound,
			"Inspect tmux pane creation and retry the spawn",
		)
		return output, nil
	}
	if err := validateGrokSpawnPaneBaselines(panes, opts); err != nil {
		output.Error = fmt.Sprintf("validating Grok Build launch panes: %v", err)
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Use an idle shell pane before launching Grok Build",
		)
		return output, nil
	}

	// Apply tiled layout
	if err := deps.ApplyTiledLayout(ctx, opts.Session); err != nil {
		if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
			setSpawnCancellation(output, cancelErr)
		} else {
			output.Error = fmt.Sprintf("applying tiled layout: %v", err)
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Inspect tmux layout support and retry")
		}
		return output, nil
	}

	// Initialize agent name map
	var nameMap *AgentNameMap
	if len(opts.CustomNames) > 0 {
		nameMap = NewAgentNameMapWithCustomNames(opts.Session, opts.CustomNames)
	} else {
		nameMap = NewAgentNameMap(opts.Session)
	}

	// Start assigning agents (skip first pane if user pane)
	startIdx := 0
	if !opts.NoUserPane {
		startIdx = 1
		// Add user pane info
		if len(panes) > 0 {
			userPaneRef := panes[0].Ref().Physical()
			userName := nameMap.AssignNew("user", userPaneRef)
			output.Agents = append(output.Agents, SpawnedAgent{
				Pane:      userPaneRef,
				Name:      userName,
				Type:      "user",
				Title:     panes[0].Title,
				Ready:     true,
				StartupMs: 0,
			})
		}
	}

	agentCommands := getAgentCommands(cfg)
	type launchRequest struct {
		agentType string
		number    int
	}
	launchRequests := make([]launchRequest, 0, totalAgents)
	for _, spec := range []struct {
		agentType string
		count     int
	}{
		{agentType: "claude", count: opts.CCCount},
		{agentType: "codex", count: opts.CodCount},
		{agentType: "gemini", count: opts.GmiCount},
		{agentType: "antigravity", count: opts.AgyCount},
		{agentType: "grok", count: opts.GrokCount},
	} {
		for i := 0; i < spec.count; i++ {
			launchRequests = append(launchRequests, launchRequest{agentType: spec.agentType, number: i + 1})
		}
	}

	launchErrors := make([]error, 0)
	for i, request := range launchRequests {
		if err := ctx.Err(); err != nil {
			setSpawnCancellation(output, err)
			return output, nil
		}
		pane := panes[startIdx+i]
		agent, launchErr := deps.LaunchAgent(
			ctx, pane, opts.Session, request.agentType, request.number, dir, agentCommands[request.agentType],
		)
		if agent.Pane == "" {
			agent.Pane = fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index)
		}
		if agent.Type == "" {
			agent.Type = request.agentType
		}
		if agent.Title == "" {
			agent.Title = fmt.Sprintf("%s__%s_%d", opts.Session, agentTypeShort(request.agentType), request.number)
		}
		agent.Name = nameMap.AssignNew(request.agentType, agent.Pane)
		if launchErr != nil && agent.Error == "" {
			agent.Error = launchErr.Error()
		}
		output.Agents = append(output.Agents, agent)
		if cancelErr := spawnCancellationError(ctx, launchErr); cancelErr != nil {
			setSpawnCancellation(output, cancelErr)
			return output, nil
		}
		if launchErr != nil {
			launchErrors = append(launchErrors, fmt.Errorf("%s agent %d: %w", request.agentType, request.number, launchErr))
		}
	}
	if len(launchErrors) > 0 {
		launchErr := errors.Join(launchErrors...)
		output.Error = fmt.Sprintf("%d of %d agent launches failed: %v", len(launchErrors), totalAgents, launchErr)
		output.RobotResponse = NewErrorResponse(
			launchErr,
			ErrCodeInternalError,
			"Inspect agents[].error; successfully launched agents remain listed",
		)
		return output, nil
	}

	// Wait for agents to be ready if requested
	if opts.WaitReady {
		timeout := opts.ReadyTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		if err := deps.WaitForReady(ctx, output, timeout); err != nil {
			if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
				setSpawnCancellation(output, cancelErr)
			} else {
				output.Error = fmt.Sprintf("waiting for agents to become ready: %v", err)
				output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Inspect agent readiness diagnostics and retry")
			}
			return output, nil
		}
	}

	// Orchestrator work assignment mode
	if opts.AssignWork {
		output.Mode = "orchestrator"
		output.AssignStrategy = assignStrategy
		assignments, assignmentErr := assignWorkToAgentsWithError(
			ctx, output, dir, opts.Session, output.AssignStrategy, cfg,
			opts.RequireReservation, opts.ReservationPaths, opts.AssignmentDeps, verifiedAssignmentPlan,
		)
		output.Assignments = assignments
		ensureSpawnAssignmentCoverage(output)
		if cancelErr := spawnCancellationError(ctx, assignmentErr); cancelErr != nil {
			setSpawnCancellation(output, cancelErr)
			return output, nil
		}
		finalizeSpawnAssignmentOutput(output)
		if err := ctx.Err(); err != nil {
			setSpawnCancellation(output, err)
			return output, nil
		}
	}
	if err := ctx.Err(); err != nil {
		setSpawnCancellation(output, err)
		return output, nil
	}

	output.TotalStartupMs = time.Since(startTime).Milliseconds()

	// Update layout based on what was created
	if sessionCreated {
		output.Layout = "tiled"
	}

	return output, nil
}

// PrintSpawn creates a session with agents and outputs structured JSON.
// This is a thin wrapper around GetSpawn() for CLI output.
func PrintSpawn(ctx context.Context, opts SpawnOptions, cfg *config.Config) error {
	output, err := GetSpawn(ctx, opts, cfg)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot spawn failed")
}

// launchAgent launches a single agent and returns its info.
func launchAgent(ctx context.Context, pane tmux.Pane, session, agentType string, num int, dir, command string) (SpawnedAgent, error) {
	startTime := time.Now()

	title := fmt.Sprintf("%s__%s_%d", session, agentTypeShort(agentType), num)
	agent := SpawnedAgent{
		Pane:  fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index),
		Type:  agentType,
		Title: title,
		Ready: false,
	}
	if err := ctx.Err(); err != nil {
		agent.Error = fmt.Sprintf("launch canceled: %v", err)
		return agent, fmt.Errorf("launch canceled: %w", err)
	}
	if agentTypeShort(agentType) == "grok" {
		if err := tmux.ValidatePaneLaunchBaseline(pane); err != nil {
			agent.Error = fmt.Sprintf("launching: %v", err)
			agent.StartupMs = time.Since(startTime).Milliseconds()
			return agent, fmt.Errorf("launching Grok Build: %w", err)
		}
	}

	// Set pane title
	if err := tmux.SetPaneTitleContext(ctx, pane.ID, title); err != nil {
		agent.Error = fmt.Sprintf("setting title: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent, fmt.Errorf("setting title: %w", err)
	}

	// Launch agent command
	safeCommand, err := tmux.SanitizePaneCommand(command)
	if err != nil {
		agent.Error = fmt.Sprintf("invalid command: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent, fmt.Errorf("invalid command: %w", err)
	}

	cmd, err := tmux.BuildPaneCommand(dir, safeCommand)
	if err != nil {
		agent.Error = fmt.Sprintf("building command: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent, fmt.Errorf("building command: %w", err)
	}
	if err := ctx.Err(); err != nil {
		agent.Error = fmt.Sprintf("launch canceled: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent, fmt.Errorf("launch canceled: %w", err)
	}

	// Use the agent-aware context path so cancellation covers staging and Enter.
	if err := tmux.SendKeysForAgentContext(ctx, pane.ID, cmd, true, tmux.AgentType(agentTypeShort(agentType))); err != nil {
		agent.Error = fmt.Sprintf("launching: %v", err)
		agent.StartupMs = time.Since(startTime).Milliseconds()
		return agent, fmt.Errorf("launching: %w", err)
	}
	if agentTypeShort(agentType) == "grok" {
		if _, err := tmux.WaitForPaneProcessStartContext(ctx, session, pane.ID); err != nil {
			agent.Error = fmt.Sprintf("launching: stable process did not start: %v", err)
			agent.StartupMs = time.Since(startTime).Milliseconds()
			return agent, fmt.Errorf("launching Grok Build: stable process did not start: %w", err)
		}
	}

	agent.StartupMs = time.Since(startTime).Milliseconds()
	return agent, nil
}

// waitForAgentsReady polls agents for ready state.
func waitForAgentsReady(ctx context.Context, output *SpawnOutput, timeout time.Duration) error {
	return waitForAgentsReadyWithCapture(ctx, output, timeout, tmux.CapturePaneOutputContext)
}

// spawnPaneLiveness reports whether a process is running under the pane's shell.
// Readiness decided from captured text alone cannot distinguish a started agent
// from a bare shell prompt, so it is corroborated with process liveness — the
// same gate the restart path applies.
var spawnPaneLiveness = func(ctx context.Context, target string) (shellPID int, childAlive bool) {
	pid, err := paneShellPIDContext(ctx, target)
	if err != nil || pid <= 0 {
		// PID unavailable: fall back to the text verdict rather than blocking a
		// spawn that may genuinely be ready.
		return 0, false
	}
	return pid, process.HasChildAlive(pid)
}

func waitForAgentsReadyWithCapture(
	ctx context.Context,
	output *SpawnOutput,
	timeout time.Duration,
	capture func(context.Context, string, int) (string, error),
) error {
	if ctx == nil {
		return errors.New("waiting for agents requires a context")
	}
	if output == nil || capture == nil {
		return errors.New("waiting for agents requires spawn output and capture dependency")
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := readyCtx.Deadline()
	pollInterval := 500 * time.Millisecond

	for {
		if err := readyCtx.Err(); err != nil {
			if parentErr := ctx.Err(); parentErr != nil {
				return parentErr
			}
			return fmt.Errorf("agents not ready before %s timeout: %w", timeout, err)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("agents not ready before %s timeout: %w", timeout, context.DeadlineExceeded)
		}
		allReady := true

		for i := range output.Agents {
			if output.Agents[i].Type == "user" {
				continue // User pane is always ready
			}
			if output.Agents[i].Ready {
				continue // Already detected as ready
			}

			// Build tmux target from session and pane reference
			// The Pane field is in "window.index" format (e.g., "0.2")
			// For tmux capture, use "session:window.pane" format
			paneRef := output.Agents[i].Pane

			// We can use the paneRef directly as it contains window.index
			target := fmt.Sprintf("%s:%s", output.Session, paneRef)

			// Capture pane output (50 lines to catch Claude's TUI)
			captured, err := capture(readyCtx, target, 50)
			if err != nil {
				if waitErr := readyCtx.Err(); waitErr != nil {
					if parentErr := ctx.Err(); parentErr != nil {
						return parentErr
					}
					return fmt.Errorf("agents not ready before %s timeout: %w", timeout, waitErr)
				}
				if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
					return cancelErr
				}
				allReady = false
				continue
			}

			// Check for ready indicators, then corroborate with process liveness.
			// Text alone reports a bare shell prompt as ready, so a spawn whose
			// agent CLI is missing from PATH used to return ready:true with
			// nothing running at all.
			ready := isAgentReady(captured, output.Agents[i].Type)
			if ready {
				if shellPID, childAlive := spawnPaneLiveness(readyCtx, target); shellPID > 0 && !childAlive {
					ready = false
				}
			}
			if ready {
				output.Agents[i].Ready = true
			} else {
				allReady = false
			}
		}

		if allReady {
			return nil
		}

		wait := time.Until(deadline)
		if wait > pollInterval {
			wait = pollInterval
		}
		timer := time.NewTimer(wait)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			if parentErr := ctx.Err(); parentErr != nil {
				return parentErr
			}
			return fmt.Errorf("agents not ready before %s timeout: %w", timeout, readyCtx.Err())
		case <-timer.C:
		}
	}
}

// isAgentReady checks if agent output indicates ready state.
// Note: agentType is accepted for future type-specific detection but currently unused.
func isAgentReady(output, _ string) bool {
	lower := strings.ToLower(output)

	// Common ready indicators (case-insensitive)
	lowerPatterns := []string{
		"claude>",
		"claude >",
		"codex>",
		"openai codex",
		"context left",
		"gemini>",
		">>>", // Python REPL
		"waiting for input",
		"how can i help",
		// Claude Code TUI indicators
		"claude code v",      // Version banner
		"welcome back",       // Greeting
		"bypass permissions", // Status line
		"try \"",             // Example prompt
	}

	for _, pattern := range lowerPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}

	// "ready" needs a word boundary: as a bare substring it also matched
	// "already", so ordinary output such as "Already up to date." counted as an
	// agent coming up.
	if readyWordRe.MatchString(lower) {
		return true
	}

	for _, p := range promptPatterns {
		if p.MatchString(output) {
			return true
		}
	}

	return false
}

// agentTypeShort returns short form for pane naming.
func agentTypeShort(agentType string) string {
	switch tmux.AgentType(agentType).Canonical() {
	case tmux.AgentClaude:
		return "cc"
	case tmux.AgentCodex:
		return "cod"
	case tmux.AgentGemini:
		return "gmi"
	case tmux.AgentAntigravity:
		return "agy"
	case tmux.AgentGrok:
		return "grok"
	case tmux.AgentCursor:
		return "cursor"
	case tmux.AgentWindsurf:
		return "windsurf"
	case tmux.AgentAider:
		return "aider"
	case tmux.AgentOllama:
		return "ollama"
	case tmux.AgentUser:
		return "user"
	default:
		return strings.TrimSpace(agentType)
	}
}

// getAgentCommands returns the commands to launch each agent type.
func getAgentCommands(cfg *config.Config) map[string]string {
	defaults := map[string]string{
		"claude":      "claude",
		"codex":       "codex",
		"gemini":      "gemini",
		"antigravity": "agy",
		"grok":        "grok --always-approve",
	}

	if cfg != nil && cfg.Agents.Claude != "" {
		defaults["claude"] = cfg.Agents.Claude
	}
	if cfg != nil && cfg.Agents.Codex != "" {
		defaults["codex"] = cfg.Agents.Codex
	}
	if cfg != nil && cfg.Agents.Gemini != "" {
		defaults["gemini"] = cfg.Agents.Gemini
	}
	if cfg != nil && cfg.Agents.Antigravity != "" {
		defaults["antigravity"] = cfg.Agents.Antigravity
	}
	if cfg != nil && cfg.Agents.Grok != "" {
		defaults["grok"] = cfg.Agents.Grok
	}

	for agentType, cmdTemplate := range defaults {
		vars := config.AgentTemplateVars{AgentType: agentType}
		if cfg != nil {
			// Resolve the default model for every agent type, exactly as the
			// interactive spawn path does via ResolveModel. Limiting this to grok
			// left the others rendering with an empty Model: harmless for the
			// templates that guard with {{if .Model}}, but agy's template injects
			// --model unconditionally because its model is hard-pinned, so a
			// robot-spawned agy pane launched as `--model ''` and never started.
			vars.Model = cfg.Models.GetModelName(agentType, "")
		}
		if rendered, err := config.GenerateAgentCommand(cmdTemplate, vars); err == nil {
			defaults[agentType] = rendered
		}
		// On error, keep original command (non-template or invalid template)
	}

	return defaults
}

// loadLatestHandoff loads the most recent handoff for a session and returns recovery context.
// Returns nil if no handoff is found or an error occurs (non-fatal).
func loadLatestHandoff(workDir, sessionName string) (*SpawnRecovery, *recovery.HandoffContext) {
	reader := handoff.NewReader(workDir)
	h, path, err := reader.FindLatest(sessionName)
	if err != nil || h == nil {
		return nil, nil
	}

	// Convert to recovery context
	ctx := recovery.HandoffContextFromHandoff(h, path)
	if ctx == nil {
		return nil, nil
	}

	// Format the injection text for fresh spawn
	injectedText := recovery.GetInjectionForType(recovery.SessionFreshSpawn, ctx, nil)

	// Build spawn recovery info
	spawnRecovery := &SpawnRecovery{
		HandoffPath:  path,
		HandoffAge:   recovery.HumanizeDuration(ctx.Age),
		Goal:         ctx.Goal,
		Now:          ctx.Now,
		Status:       ctx.Status,
		Outcome:      ctx.Outcome,
		InjectedText: injectedText,
	}

	return spawnRecovery, ctx
}

func normalizeAssignStrategyStrict(strategy string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(strategy))
	switch s {
	case "top-n", "topn":
		return "top-n", nil
	case "diverse":
		return "diverse", nil
	case "dependency-aware", "dependency":
		return "dependency-aware", nil
	case "skill-matched", "skill":
		return "skill-matched", nil
	case "":
		return "", errors.New("assignment strategy is required when --spawn-assign-work is enabled")
	default:
		return "", fmt.Errorf("unsupported assignment strategy %q (expected top-n, diverse, dependency-aware, or skill-matched)", strategy)
	}
}

// assignWorkToAgents gets triage recommendations, claims beads, and sends work prompts.
func assignWorkToAgents(ctx context.Context, output *SpawnOutput, workDir, session, strategy string, cfg *config.Config, requireReservation bool, reservationPaths []string, customDeps *SpawnAssignmentDependencies) []SpawnAssignment {
	assignments, _ := assignWorkToAgentsWithError(
		ctx, output, workDir, session, strategy, cfg, requireReservation, reservationPaths, customDeps, nil,
	)
	return assignments
}

func assignWorkToAgentsWithError(ctx context.Context, output *SpawnOutput, workDir, session, strategy string, cfg *config.Config, requireReservation bool, reservationPaths []string, customDeps *SpawnAssignmentDependencies, verifiedPlan *bv.TriageResponse) ([]SpawnAssignment, error) {
	var assignments []SpawnAssignment
	if ctx == nil {
		return []SpawnAssignment{{ClaimError: "spawn assignment context is required"}}, nil
	}
	if err := ctx.Err(); err != nil {
		return []SpawnAssignment{{ClaimError: fmt.Sprintf("spawn assignment canceled: %v", err)}}, err
	}
	deps := spawnAssignmentDeps(customDeps)

	// Get non-user agents that are ready
	var readyAgents []SpawnedAgent
	for _, agent := range output.Agents {
		if agent.Type == "user" {
			continue
		}
		// Include agents even if not marked ready (best effort)
		readyAgents = append(readyAgents, agent)
	}

	if len(readyAgents) == 0 {
		return assignments, nil
	}

	// Production preflights the verified actionable plan before creating the
	// session. Focused helper tests may still inject a complete triage fixture.
	triage := verifiedPlan
	if triage == nil {
		var err error
		triage, err = deps.FetchTriage(ctx, workDir)
		if err != nil {
			wrapped := fmt.Errorf("load bv triage: %w", err)
			return spawnAgentPlanErrors(readyAgents, wrapped), spawnCancellationError(ctx, wrapped)
		}
	}
	if triage == nil {
		return spawnAgentPlanErrors(readyAgents, errors.New("load bv triage: empty response")), nil
	}
	if err := ctx.Err(); err != nil {
		return spawnAgentPlanErrors(readyAgents, fmt.Errorf("spawn assignment canceled after triage: %w", err)), err
	}

	// Get work items based on strategy
	workItems := getWorkItemsForStrategy(triage, strategy, len(readyAgents))
	if len(workItems) == 0 {
		ledgerExists, err := deps.AssignmentLedgerExists(session)
		if err != nil {
			wrapped := fmt.Errorf("inspect assignment ledger: %w", err)
			return spawnAgentPlanErrors(readyAgents, wrapped), spawnCancellationError(ctx, wrapped)
		}
		if !ledgerExists {
			return assignments, nil
		}
		store, err := deps.LoadStore(session)
		if err != nil {
			wrapped := fmt.Errorf("load assignment ledger for replay: %w", err)
			return spawnAgentPlanErrors(readyAgents, wrapped), spawnCancellationError(ctx, wrapped)
		}
		if err := ctx.Err(); err != nil {
			return spawnAgentPlanErrors(readyAgents, fmt.Errorf("spawn assignment canceled after replay ledger load: %w", err)), err
		}
		panes, err := deps.ListPanes(ctx, session)
		if err != nil {
			wrapped := fmt.Errorf("load pane topology for assignment replay: %w", err)
			return spawnAgentPlanErrors(readyAgents, wrapped), spawnCancellationError(ctx, wrapped)
		}
		if err := ctx.Err(); err != nil {
			return spawnAgentPlanErrors(readyAgents, fmt.Errorf("spawn assignment canceled after replay topology load: %w", err)), err
		}
		return spawnDurableAssignmentReplays(
			ctx, workDir, readyAgents, panes, store, requireReservation, reservationPaths, deps,
		)
	}

	store, err := deps.LoadStore(session)
	if err != nil {
		wrapped := fmt.Errorf("load assignment ledger: %w", err)
		return spawnAssignmentPlanErrors(readyAgents, workItems, wrapped), spawnCancellationError(ctx, wrapped)
	}
	if err := ctx.Err(); err != nil {
		return spawnAssignmentPlanErrors(readyAgents, workItems, fmt.Errorf("spawn assignment canceled after ledger load: %w", err)), err
	}
	redactionConfig := config.Default().Redaction.ToRedactionLibConfig()
	if cfg != nil {
		redactionConfig = cfg.Redaction.ToRedactionLibConfig()
	}
	dispatchPort := newRobotAtomicPaneDispatchPort(session, deps.ListPanes, deps.ObserveSession, redactionConfig, deps.DispatchDeliverer, deps.DispatchPacer)
	if deps.ClaimBeadWithOperatorGatedLabels == nil {
		return spawnAssignmentPlanErrors(readyAgents, workItems, errors.New("spawn assignment guarded claim policy is unavailable")), nil
	}
	operatorGatedLabels := []string(nil)
	if cfg != nil {
		operatorGatedLabels = append(operatorGatedLabels, cfg.Assign.OperatorGatedLabels...)
	}
	claimPort := newRobotAtomicClaimPort(workDir, func(ctx context.Context, dir, beadID, actor string) (bv.BeadClaimResult, error) {
		return deps.ClaimBeadWithOperatorGatedLabels(ctx, dir, beadID, actor, operatorGatedLabels)
	})
	panes, err := deps.ListPanes(ctx, session)
	if err != nil {
		wrapped := fmt.Errorf("load pane topology: %w", err)
		return spawnAssignmentPlanErrors(readyAgents, workItems, wrapped), spawnCancellationError(ctx, wrapped)
	}
	if err := ctx.Err(); err != nil {
		return spawnAssignmentPlanErrors(readyAgents, workItems, fmt.Errorf("spawn assignment canceled after topology load: %w", err)), err
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	reservationPort := deps.ReservationPort
	resolveAgentName := deps.ResolveAgentName
	var terminalErr error
	stopForTerminalError := func(err error) bool {
		cancelErr := spawnCancellationError(ctx, err)
		if cancelErr == nil {
			return false
		}
		terminalErr = cancelErr
		return true
	}

	// Assign work to agents
	for i, agent := range readyAgents {
		if i >= len(workItems) {
			break
		}
		if err := ctx.Err(); err != nil {
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i:], fmt.Errorf("spawn assignment canceled: %w", err))...)
			terminalErr = err
			break
		}

		item := workItems[i]
		spawnAssignment := SpawnAssignment{
			Pane:      agent.Pane,
			AgentType: agent.Type,
			BeadID:    item.ID,
			BeadTitle: item.Title,
			Priority:  fmt.Sprintf("P%d", item.Priority),
		}
		resolved, resolveErr := tmux.ResolvePaneSelectors(panes, []string{agent.Pane}, true)
		if resolveErr != nil {
			spawnAssignment.ClaimError = fmt.Sprintf("resolve pane %s: %v", agent.Pane, resolveErr)
			assignments = append(assignments, spawnAssignment)
			continue
		}
		pane := resolved[0]
		spawnAssignment.Pane = pane.Ref().Canonical(multiWindow)
		target := pane.ID
		if target == "" {
			target = pane.Ref().Physical()
		}
		prompt := generateWorkPrompt(item)
		agentName := ""
		idempotencyKey := ""
		if replay := robotAtomicReplayIntent(store, item.ID, target, pane.Index, agent.Type, prompt, requireReservation, reservationPaths); replay != nil {
			agentName = replay.AgentName
			idempotencyKey = replay.IdempotencyKey
		} else {
			observation, observeErr := deps.ObserveSession(ctx, session)
			if observeErr != nil {
				spawnAssignment.ClaimError = fmt.Sprintf("observe pane %s before assignment: %v", spawnAssignment.Pane, observeErr)
				assignments = append(assignments, spawnAssignment)
				if stopForTerminalError(observeErr) {
					assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], observeErr)...)
					break
				}
				continue
			}
			if err := ctx.Err(); err != nil {
				spawnAssignment.ClaimError = fmt.Sprintf("spawn assignment canceled after observation: %v", err)
				assignments = append(assignments, spawnAssignment)
				assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], err)...)
				terminalErr = err
				break
			}
			if !observation.SafeToDispatch(target) {
				spawnAssignment.ClaimError = fmt.Sprintf("pane %s (%s) is not safe to dispatch", spawnAssignment.Pane, target)
				assignments = append(assignments, spawnAssignment)
				continue
			}

			agentName = strings.TrimSpace(agent.Name)
			if requireReservation {
				if reservationPort == nil {
					mailRuntime, runtimeErr := newRobotAgentMailReservationRuntime(ctx, workDir, session, nil)
					if runtimeErr != nil {
						spawnAssignment.ClaimError = runtimeErr.Error()
						assignments = append(assignments, spawnAssignment)
						if stopForTerminalError(runtimeErr) {
							assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], runtimeErr)...)
							break
						}
						continue
					}
					reservationPort = mailRuntime
					if resolveAgentName == nil {
						resolveAgentName = mailRuntime.ResolveRecipient
					}
				}
				if resolveAgentName == nil {
					spawnAssignment.ClaimError = "required reservation has no exact Agent Mail pane-identity resolver"
					assignments = append(assignments, spawnAssignment)
					continue
				}
				agentName, resolveErr = resolveAgentName(ctx, workDir, session, target, pane.Title)
				if resolveErr != nil {
					spawnAssignment.ClaimError = resolveErr.Error()
					assignments = append(assignments, spawnAssignment)
					if stopForTerminalError(resolveErr) {
						assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], resolveErr)...)
						break
					}
					continue
				}
				if err := ctx.Err(); err != nil {
					spawnAssignment.ClaimError = fmt.Sprintf("spawn assignment canceled after reservation identity resolution: %v", err)
					assignments = append(assignments, spawnAssignment)
					assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], err)...)
					terminalErr = err
					break
				}
				agentName = strings.TrimSpace(agentName)
			}
			if agentName == "" {
				spawnAssignment.ClaimError = fmt.Sprintf("pane %s (%s) has no canonical assignment identity", spawnAssignment.Pane, target)
				assignments = append(assignments, spawnAssignment)
				continue
			}
			var keyErr error
			idempotencyKey, keyErr = robotAtomicIdempotencyKey(
				store, item.ID, target, pane.Index, agent.Type, agentName, prompt,
				requireReservation, reservationPaths, deps.NewIdempotencyKey,
			)
			if keyErr != nil {
				spawnAssignment.ClaimError = keyErr.Error()
				assignments = append(assignments, spawnAssignment)
				if stopForTerminalError(keyErr) {
					assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], keyErr)...)
					break
				}
				continue
			}
		}
		spawnAssignment.IdempotencyKey = idempotencyKey
		if err := ctx.Err(); err != nil {
			spawnAssignment.ClaimError = fmt.Sprintf("spawn assignment canceled before atomic claim: %v", err)
			assignments = append(assignments, spawnAssignment)
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], err)...)
			terminalErr = err
			break
		}
		if deps.GetBeadDetails == nil {
			spawnAssignment.ClaimError = "spawn assignment exact eligibility reader is unavailable"
			assignments = append(assignments, spawnAssignment)
			continue
		}
		coordinator := assignment.NewAtomicCoordinator(store, claimPort, reservationPort, dispatchPort, dispatchPort).
			WithWorkItemStatusPort(assignment.WorkItemStatusFunc(func(statusCtx context.Context, beadID string) (string, error) {
				if err := statusCtx.Err(); err != nil {
					return "", err
				}
				return deps.GetBeadStatus(statusCtx, workDir, beadID)
			}))
		coordinator = coordinator.WithAssignmentEligibilityAuthorizationPort(
			newRobotAtomicEligibilityAuthorizationPort(workDir, operatorGatedLabels, deps.GetBeadDetails),
		)
		result, executeErr := coordinator.Execute(ctx, spawnAtomicRequest(
			item, target, pane.Index, agent.Type, agentName, prompt, idempotencyKey, requireReservation, reservationPaths,
		))
		if result.Assignment != nil && result.Assignment.IdempotencyKey == idempotencyKey {
			spawnAssignment.Claimed = result.Assignment.ClaimState == assignment.ClaimClaimed
			spawnAssignment.ClaimActor = result.Assignment.ClaimActor
			spawnAssignment.DispatchReceiptID = result.Assignment.DispatchReceiptID
			spawnAssignment.ReservationIDs = append([]int(nil), result.Assignment.ReservationIDs...)
		}
		if executeErr != nil {
			if spawnAssignment.Claimed {
				spawnAssignment.PromptError = executeErr.Error()
			} else {
				spawnAssignment.ClaimError = executeErr.Error()
			}
		} else {
			spawnAssignment.PromptSent = result.Sent
		}

		assignments = append(assignments, spawnAssignment)
		if stopForTerminalError(executeErr) {
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], executeErr)...)
			break
		}
		if err := ctx.Err(); err != nil {
			assignments = append(assignments, spawnAgentPlanErrors(readyAgents[i+1:], err)...)
			terminalErr = err
			break
		}
	}

	return assignments, terminalErr
}

func spawnAgentPlanErrors(agents []SpawnedAgent, err error) []SpawnAssignment {
	result := make([]SpawnAssignment, 0, len(agents))
	for _, agent := range agents {
		result = append(result, SpawnAssignment{
			Pane: agent.Pane, AgentType: agent.Type, ClaimError: err.Error(),
		})
	}
	return result
}

func ensureSpawnAssignmentCoverage(output *SpawnOutput) {
	if output == nil {
		return
	}
	multiWindow := spawnCoverageSpansMultipleWindows(output.Agents)
	represented := make(map[string]struct{}, len(output.Assignments))
	for _, spawnAssignment := range output.Assignments {
		represented[spawnCoveragePaneKey(spawnAssignment.Pane, multiWindow)] = struct{}{}
	}
	for _, agent := range output.Agents {
		if agent.Type == "user" {
			continue
		}
		coverageKey := spawnCoveragePaneKey(agent.Pane, multiWindow)
		if _, ok := represented[coverageKey]; ok {
			continue
		}
		output.Assignments = append(output.Assignments, SpawnAssignment{
			Pane:       agent.Pane,
			AgentType:  agent.Type,
			ClaimError: "no work assignment was produced for this eligible agent",
		})
		represented[coverageKey] = struct{}{}
	}
}

func spawnCoverageSpansMultipleWindows(agents []SpawnedAgent) bool {
	firstWindow := 0
	haveWindow := false
	for _, agent := range agents {
		if agent.Type == "user" {
			continue
		}
		selector, err := tmux.ParsePaneSelector(agent.Pane)
		if err != nil || selector.Kind != tmux.PaneSelectorWindowPane {
			continue
		}
		if !haveWindow {
			firstWindow = selector.WindowIndex
			haveWindow = true
			continue
		}
		if selector.WindowIndex != firstWindow {
			return true
		}
	}
	return false
}

func spawnCoveragePaneKey(raw string, multiWindow bool) string {
	selector, err := tmux.ParsePaneSelector(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	switch selector.Kind {
	case tmux.PaneSelectorWindowPane:
		if multiWindow {
			return fmt.Sprintf("%d.%d", selector.WindowIndex, selector.PaneIndex)
		}
		return fmt.Sprint(selector.PaneIndex)
	case tmux.PaneSelectorPaneIndex:
		return fmt.Sprint(selector.Index)
	case tmux.PaneSelectorID:
		return selector.PaneID
	default:
		return strings.TrimSpace(raw)
	}
}

func finalizeSpawnAssignmentOutput(output *SpawnOutput) {
	if output == nil {
		return
	}
	ensureSpawnAssignmentCoverage(output)
	failed := 0
	for _, spawnAssignment := range output.Assignments {
		if spawnAssignment.ClaimError != "" || spawnAssignment.PromptError != "" || !spawnAssignment.Claimed || !spawnAssignment.PromptSent {
			failed++
		}
	}
	if failed == 0 {
		return
	}
	output.Error = fmt.Sprintf("%d of %d spawn work assignments failed", failed, len(output.Assignments))
	output.RobotResponse = NewErrorResponse(
		fmt.Errorf("%s", output.Error),
		"ASSIGNMENT_FAILED",
		"Inspect assignments[].claim_error and assignments[].prompt_error; failed targets were not dispatched",
	)
}

func spawnAtomicRequest(item workItem, target string, pane int, agentType, agentName, prompt, key string, requireReservation bool, reservationPaths []string) assignment.AtomicRequest {
	return assignment.AtomicRequest{
		BeadID: item.ID, BeadTitle: item.Title, Target: target, OccupancyKey: target, Pane: pane,
		AgentType: agentType, AgentName: agentName, Actor: agentName, Prompt: prompt,
		IdempotencyKey: key, RequireReservation: requireReservation, ReservationTTL: time.Hour,
		RequestedPaths: append([]string(nil), reservationPaths...),
	}
}

func spawnAssignmentPlanErrors(agents []SpawnedAgent, items []workItem, err error) []SpawnAssignment {
	limit := len(agents)
	if len(items) < limit {
		limit = len(items)
	}
	result := make([]SpawnAssignment, 0, limit)
	for i := 0; i < limit; i++ {
		result = append(result, SpawnAssignment{
			Pane: agents[i].Pane, AgentType: agents[i].Type, BeadID: items[i].ID,
			BeadTitle: items[i].Title, Priority: fmt.Sprintf("P%d", items[i].Priority),
			ClaimError: err.Error(),
		})
	}
	return result
}

func spawnAssignmentLedgerExists(session string) (bool, error) {
	path := filepath.Join(assignment.StorageDir(), session, "assignments.json")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("assignment ledger %s is not a regular file", path)
	}
	return true, nil
}

// spawnDurableAssignmentReplays reports exact completed dispatch receipts when
// the freshly verified actionable plan is empty. It never claims, reserves, or
// dispatches; the active Beads owner and durable intent must still agree.
func spawnDurableAssignmentReplays(
	ctx context.Context,
	workDir string,
	agents []SpawnedAgent,
	panes []tmux.Pane,
	store *assignment.AssignmentStore,
	requireReservation bool,
	reservationPaths []string,
	deps SpawnAssignmentDependencies,
) ([]SpawnAssignment, error) {
	if store == nil {
		return spawnAgentPlanErrors(agents, errors.New("assignment replay ledger is unavailable")), nil
	}

	durable := store.GetAll()
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	result := make([]SpawnAssignment, 0, len(agents))
	for i, agent := range agents {
		if err := ctx.Err(); err != nil {
			wrapped := fmt.Errorf("spawn assignment replay canceled: %w", err)
			result = append(result, spawnAgentPlanErrors(agents[i:], wrapped)...)
			return result, err
		}

		resolved, err := tmux.ResolvePaneSelectors(panes, []string{agent.Pane}, true)
		if err != nil {
			result = append(result, SpawnAssignment{
				Pane: agent.Pane, AgentType: agent.Type,
				ClaimError: fmt.Sprintf("resolve pane %s for assignment replay: %v", agent.Pane, err),
			})
			continue
		}
		pane := resolved[0]
		target := strings.TrimSpace(pane.ID)
		if target == "" {
			target = pane.Ref().Physical()
		}

		candidates := make([]assignment.Assignment, 0, 1)
		for _, existing := range durable {
			if robotAtomicAssignmentTerminal(existing.Status) {
				continue
			}
			occupancy := strings.TrimSpace(existing.OccupancyKey)
			if occupancy == "" {
				occupancy = strings.TrimSpace(existing.DispatchTarget)
			}
			if strings.TrimSpace(existing.DispatchTarget) == target || occupancy == target {
				candidates = append(candidates, existing)
			}
		}
		paneRef := pane.Ref().Canonical(multiWindow)
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) != 1 {
			result = append(result, SpawnAssignment{
				Pane: paneRef, AgentType: agent.Type,
				ClaimError: fmt.Sprintf("assignment replay target %s has %d active durable records", target, len(candidates)),
			})
			continue
		}

		replay, err := spawnDurableAssignmentReplay(
			ctx, workDir, agent, pane, paneRef, target, candidates[0],
			requireReservation, reservationPaths, deps.GetBeadDetails,
		)
		if err != nil {
			result = append(result, SpawnAssignment{
				Pane: paneRef, AgentType: agent.Type, BeadID: candidates[0].BeadID,
				BeadTitle: candidates[0].BeadTitle, ClaimError: err.Error(),
			})
			if cancelErr := spawnCancellationError(ctx, err); cancelErr != nil {
				result = append(result, spawnAgentPlanErrors(agents[i+1:], err)...)
				return result, cancelErr
			}
			continue
		}
		result = append(result, replay)
	}
	return result, nil
}

func spawnDurableAssignmentReplay(
	ctx context.Context,
	workDir string,
	agent SpawnedAgent,
	pane tmux.Pane,
	paneRef, target string,
	existing assignment.Assignment,
	requireReservation bool,
	reservationPaths []string,
	getBeadDetails func(context.Context, string, string) (*bv.BeadAssignmentDetails, error),
) (SpawnAssignment, error) {
	occupancy := strings.TrimSpace(existing.OccupancyKey)
	if occupancy == "" {
		occupancy = strings.TrimSpace(existing.DispatchTarget)
	}
	promptChecksum := assignment.PromptSHA256(existing.PromptSent)
	if existing.Pane != pane.Index || strings.TrimSpace(existing.DispatchTarget) != target || occupancy != target ||
		normalizeAgentType(existing.AgentType) != normalizeAgentType(agent.Type) {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s does not match pane %s identity", existing.BeadID, target)
	}
	if strings.TrimSpace(existing.IdempotencyKey) == "" || strings.TrimSpace(existing.ClaimActor) == "" ||
		existing.ClaimState != assignment.ClaimClaimed || existing.DispatchState != assignment.DispatchSent ||
		strings.TrimSpace(existing.DispatchReceiptID) == "" {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s has no complete claim and dispatch receipt", existing.BeadID)
	}
	if existing.ReservationRequired != requireReservation ||
		!stringSlicesEqualRobot(existing.ReservationInputPaths, reservationPaths) {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s does not match the requested reservation contract", existing.BeadID)
	}
	if requireReservation && (existing.ReservationState != assignment.ReservationReserved || !existing.ReservationCompleted) {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s has no complete reservation receipt", existing.BeadID)
	}
	if existing.PromptSent == "" || strings.TrimSpace(existing.IntentSHA256) == "" ||
		strings.TrimSpace(existing.PromptSHA256) == "" || existing.PromptSHA256 != promptChecksum {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s has no verifiable sent prompt checksum", existing.BeadID)
	}
	if getBeadDetails == nil {
		return SpawnAssignment{}, errors.New("assignment replay requires an exact Beads state reader")
	}
	details, err := getBeadDetails(ctx, workDir, existing.BeadID)
	if err != nil {
		return SpawnAssignment{}, fmt.Errorf("verify durable assignment %s owner: %w", existing.BeadID, err)
	}
	if details == nil || details.ID != existing.BeadID ||
		!strings.EqualFold(strings.TrimSpace(details.Status), "in_progress") ||
		strings.TrimSpace(details.Assignee) != strings.TrimSpace(existing.ClaimActor) {
		return SpawnAssignment{}, fmt.Errorf("durable assignment %s is not owned in_progress by %s", existing.BeadID, existing.ClaimActor)
	}

	return SpawnAssignment{
		Pane:              paneRef,
		AgentType:         agent.Type,
		BeadID:            existing.BeadID,
		BeadTitle:         existing.BeadTitle,
		Priority:          fmt.Sprintf("P%d", details.Priority),
		Claimed:           true,
		PromptSent:        true,
		ClaimActor:        existing.ClaimActor,
		IdempotencyKey:    existing.IdempotencyKey,
		DispatchReceiptID: existing.DispatchReceiptID,
		ReservationIDs:    append([]int(nil), existing.ReservationIDs...),
	}, nil
}

func spawnAssignmentDeps(custom *SpawnAssignmentDependencies) SpawnAssignmentDependencies {
	observer := statuspkg.NewSessionObserver(statuspkg.NewDetector())
	deps := SpawnAssignmentDependencies{
		LoadAssignmentPolicy:             loadAuthoritativeAssignmentPolicy,
		FetchActionable:                  getAssignableActionableRecommendations,
		FetchTriage:                      bv.GetTriageContext,
		AssignmentLedgerExists:           spawnAssignmentLedgerExists,
		ListPanes:                        tmux.GetPanesContext,
		LoadStore:                        assignment.LoadStoreStrict,
		ClaimBead:                        bv.ClaimBeadForAssignment,
		ClaimBeadWithOperatorGatedLabels: bv.ClaimBeadForAssignmentWithOperatorGatedLabels,
		GetBeadStatus:                    bv.GetBeadStatusContext,
		GetBeadDetails:                   bv.GetBeadAssignmentDetailsContext,
		NewIdempotencyKey:                assignment.NewAssignmentIdempotencyKey,
		ObserveSession:                   observer.Observe,
		DispatchDeliverer:                dispatchsvc.TMUXDeliverer{},
	}
	if custom == nil {
		return deps
	}
	if custom.LoadAssignmentPolicy != nil {
		deps.LoadAssignmentPolicy = custom.LoadAssignmentPolicy
	}
	if custom.FetchActionable != nil {
		deps.FetchActionable = custom.FetchActionable
	}
	if custom.FetchTriage != nil {
		deps.FetchTriage = custom.FetchTriage
		if custom.FetchActionable == nil {
			// Focused helper tests inject complete triage fixtures. Production
			// always uses the verified actionable loader before session creation.
			deps.FetchActionable = func(ctx context.Context, dir string, limit int) ([]bv.TriageRecommendation, error) {
				triage, err := custom.FetchTriage(ctx, dir)
				if err != nil {
					return nil, err
				}
				if triage == nil {
					return []bv.TriageRecommendation{}, nil
				}
				return filterAssignableActionableRecommendationsForProject(dir, triage.Triage.Recommendations, limit), nil
			}
		}
	}
	if custom.AssignmentLedgerExists != nil {
		deps.AssignmentLedgerExists = custom.AssignmentLedgerExists
	}
	if custom.ListPanes != nil {
		deps.ListPanes = custom.ListPanes
	}
	if custom.LoadStore != nil {
		deps.LoadStore = custom.LoadStore
	}
	if custom.ClaimBead != nil {
		deps.ClaimBead = custom.ClaimBead
		deps.ClaimBeadWithOperatorGatedLabels = func(ctx context.Context, dir, beadID, actor string, _ []string) (bv.BeadClaimResult, error) {
			return custom.ClaimBead(ctx, dir, beadID, actor)
		}
	}
	if custom.ClaimBeadWithOperatorGatedLabels != nil {
		deps.ClaimBeadWithOperatorGatedLabels = custom.ClaimBeadWithOperatorGatedLabels
	}
	if custom.GetBeadStatus != nil {
		deps.GetBeadStatus = custom.GetBeadStatus
	}
	if custom.GetBeadDetails != nil {
		deps.GetBeadDetails = custom.GetBeadDetails
	} else if custom.ClaimBead != nil {
		// Legacy focused tests that replace the guarded claim have no live br
		// database. Production and policy-focused tests keep the exact reader.
		deps.GetBeadDetails = nil
	}
	if custom.NewIdempotencyKey != nil {
		deps.NewIdempotencyKey = custom.NewIdempotencyKey
	}
	if custom.ReservationPort != nil {
		deps.ReservationPort = custom.ReservationPort
	}
	if custom.ResolveAgentName != nil {
		deps.ResolveAgentName = custom.ResolveAgentName
	}
	if custom.ObserveSession != nil {
		deps.ObserveSession = custom.ObserveSession
	}
	if custom.DispatchDeliverer != nil {
		deps.DispatchDeliverer = custom.DispatchDeliverer
	}
	if custom.DispatchPacer != nil {
		deps.DispatchPacer = custom.DispatchPacer
	}
	return deps
}

// workItem represents a work item from triage for assignment.
type workItem struct {
	ID       string
	Title    string
	Priority int
	Score    float64
	Type     string
	Reasons  []string
}

// getWorkItemsForStrategy returns work items based on the selected strategy.
func getWorkItemsForStrategy(triage *bv.TriageResponse, strategy string, count int) []workItem {
	var items []workItem

	switch strategy {
	case "diverse":
		// Get a mix of different task types
		items = getDiverseWorkItems(triage, count)
	case "dependency-aware":
		// Prioritize items that unblock others
		items = getDependencyAwareItems(triage, count)
	case "skill-matched":
		// This would ideally match agent types to task types
		// For now, fall through to top-n
		fallthrough
	case "top-n":
		fallthrough
	default:
		// Get top N recommendations by score
		items = getTopNWorkItems(triage, count)
	}

	return items
}

// getTopNWorkItems returns the top N recommendations by score.
func getTopNWorkItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem

	for i, rec := range triage.Triage.Recommendations {
		if i >= count {
			break
		}
		items = append(items, workItem{
			ID:       rec.ID,
			Title:    rec.Title,
			Priority: rec.Priority,
			Score:    rec.Score,
			Type:     rec.Type,
			Reasons:  rec.Reasons,
		})
	}

	return items
}

// getDiverseWorkItems returns a diverse set of work items by type.
func getDiverseWorkItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem
	seenTypes := make(map[string]bool)

	// First pass: get one of each type
	for _, rec := range triage.Triage.Recommendations {
		if len(items) >= count {
			break
		}
		if !seenTypes[rec.Type] {
			items = append(items, workItem{
				ID:       rec.ID,
				Title:    rec.Title,
				Priority: rec.Priority,
				Score:    rec.Score,
				Type:     rec.Type,
				Reasons:  rec.Reasons,
			})
			seenTypes[rec.Type] = true
		}
	}

	// Second pass: fill remaining slots with top items
	if len(items) < count {
		for _, rec := range triage.Triage.Recommendations {
			if len(items) >= count {
				break
			}
			// Check if already included
			found := false
			for _, existing := range items {
				if existing.ID == rec.ID {
					found = true
					break
				}
			}
			if !found {
				items = append(items, workItem{
					ID:       rec.ID,
					Title:    rec.Title,
					Priority: rec.Priority,
					Score:    rec.Score,
					Type:     rec.Type,
					Reasons:  rec.Reasons,
				})
			}
		}
	}

	return items
}

// getDependencyAwareItems prioritizes items that unblock the most work.
func getDependencyAwareItems(triage *bv.TriageResponse, count int) []workItem {
	var items []workItem

	// First, add blockers to clear (these unblock other work)
	for _, blocker := range triage.Triage.BlockersToClear {
		if len(items) >= count {
			break
		}
		if blocker.Actionable {
			items = append(items, workItem{
				ID:       blocker.ID,
				Title:    blocker.Title,
				Priority: 0, // Blockers get high priority
				Score:    float64(blocker.UnblocksCount),
				Type:     "blocker",
				Reasons:  []string{fmt.Sprintf("Unblocks %d items", blocker.UnblocksCount)},
			})
		}
	}

	// Then fill with top recommendations
	if len(items) < count {
		for _, rec := range triage.Triage.Recommendations {
			if len(items) >= count {
				break
			}
			// Check if already included
			found := false
			for _, existing := range items {
				if existing.ID == rec.ID {
					found = true
					break
				}
			}
			if !found {
				items = append(items, workItem{
					ID:       rec.ID,
					Title:    rec.Title,
					Priority: rec.Priority,
					Score:    rec.Score,
					Type:     rec.Type,
					Reasons:  rec.Reasons,
				})
			}
		}
	}

	return items
}

// generateWorkPrompt creates a prompt for an agent to work on a bead.
func generateWorkPrompt(item workItem) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Work on bead %s: %s\n\n", item.ID, item.Title))
	sb.WriteString("Use `br show " + item.ID + "` to see full details.\n")
	sb.WriteString("This bead has been marked as in_progress.\n")

	if len(item.Reasons) > 0 {
		sb.WriteString("\nContext:\n")
		for _, reason := range item.Reasons {
			sb.WriteString("- " + reason + "\n")
		}
	}

	sb.WriteString("\nWhen done, close it with: `br close " + item.ID + " --reason \"Completed\"`")

	return sb.String()
}
