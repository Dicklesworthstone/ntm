package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	// restartPaneSettleDelay is how long to wait after respawn-pane -k before
	// typing into the fresh shell (the shell needs a moment to initialize).
	restartPaneSettleDelay = 750 * time.Millisecond
	// restartPaneReadyTimeout bounds the post-relaunch ready-gate: we poll for
	// the agent TUI instead of sleeping a fixed interval (#187).
	restartPaneReadyTimeout = 15 * time.Second
	// restartPaneReadyPollInterval is the ready-gate poll cadence.
	restartPaneReadyPollInterval = 400 * time.Millisecond
	// restartPaneDispatchReadyTimeout bounds the canonical idle observation gate
	// that runs after relaunch readiness but before an atomic bead claim.
	restartPaneDispatchReadyTimeout = 15 * time.Second
	// restartPaneMutationObservationTimeout bounds the independent PID probe used
	// after a canceled tmux command. The caller context is already canceled at
	// that point, but we still need a short observation window to report whether
	// respawn-pane changed the pane before returning the cancellation error.
	restartPaneMutationObservationTimeout = 2 * time.Second
)

// RestartPaneOutput is the structured output for --robot-restart-pane
type RestartPaneOutput struct {
	RobotResponse
	Session             string                                 `json:"session"`
	RestartedAt         time.Time                              `json:"restarted_at"`
	Restarted           []string                               `json:"restarted"`
	Failed              []RestartError                         `json:"failed"`
	DryRun              bool                                   `json:"dry_run,omitempty"`
	WouldAffect         []string                               `json:"would_affect,omitempty"`
	BeadAssigned        string                                 `json:"bead_assigned,omitempty"` // Bead ID if --bead was used
	PromptSent          bool                                   `json:"prompt_sent,omitempty"`   // True only when every attempted ordinary prompt or the atomic bead prompt has confirmed delivery
	PromptError         string                                 `json:"prompt_error,omitempty"`  // Prompt delivery error details
	PromptDelivery      map[string]RestartPromptDeliveryStatus `json:"prompt_delivery,omitempty"`
	ProcessAlive        map[string]bool                        `json:"process_alive,omitempty"` // Running foreground process evidence; readiness is reported separately, including when an agent execs over the pane root shell.
	ClaimActor          string                                 `json:"claim_actor,omitempty"`
	IdempotencyKey      string                                 `json:"idempotency_key,omitempty"`
	DispatchReceiptID   string                                 `json:"dispatch_receipt_id,omitempty"`
	AssignmentReplayed  bool                                   `json:"assignment_replayed,omitempty"`
	AssignmentRecovered bool                                   `json:"assignment_recovered,omitempty"`
	// AgentRelaunched reports, per agent pane, whether the agent CLI was
	// relaunched after respawn and became ready. respawn-pane -k only restores
	// the pane's default command (the login shell); in ntm sessions the agent
	// CLI is started by keystroke after spawn, so it must be relaunched
	// explicitly (#187). User/unknown panes are not included.
	AgentRelaunched     map[string]bool                       `json:"agent_relaunched,omitempty"`
	AgentRelaunchStatus map[string]RestartAgentRelaunchStatus `json:"agent_relaunch_status,omitempty"`
	LaunchAffinity      map[string]resilience.LaunchAffinity  `json:"launch_affinity,omitempty"`

	// PaneShellPIDs is the per-pane respawn evidence (ntm-tgkb): a real
	// respawn replaces the pane's shell, so after MUST differ from before.
	// Operators previously diffed `tmux list-panes -F '#{pane_pid}'` by hand
	// to catch soft restarts; a success with an unchanged PID is now
	// reported as a failure instead of counted as restarted.
	PaneShellPIDs map[string]RestartPanePIDs `json:"pane_shell_pids,omitempty"`
	// Preflight is an opaque, ephemeral confirmation token. It carries no
	// runtime environment values and is never part of robot JSON output.
	Preflight *RestartPanePreflight `json:"-"`
}

// RestartPanePIDs carries the shell PID before and after a pane respawn.
// After is 0 when the post-respawn probe could not observe the new PID.
type RestartPanePIDs struct {
	Before int `json:"before"`
	After  int `json:"after,omitempty"`
}

// RestartAgentRelaunchStatus distinguishes confirmed readiness from a live
// child whose readiness could not be confirmed after cancellation.
type RestartAgentRelaunchStatus string

const (
	RestartAgentRelaunchReady    RestartAgentRelaunchStatus = "ready"
	RestartAgentRelaunchNotReady RestartAgentRelaunchStatus = "not_ready"
	RestartAgentRelaunchUnknown  RestartAgentRelaunchStatus = "unknown"
	RestartAgentRelaunchFailed   RestartAgentRelaunchStatus = "failed"
)

// RestartPromptDeliveryStatus is the strongest fact known about an ordinary
// post-restart prompt. Unknown means text or an Enter may already have reached
// the pane, so callers must inspect it before retrying.
type RestartPromptDeliveryStatus string

const (
	RestartPromptDelivered RestartPromptDeliveryStatus = "delivered"
	RestartPromptFailed    RestartPromptDeliveryStatus = "failed"
	RestartPromptSkipped   RestartPromptDeliveryStatus = "skipped"
	RestartPromptUnknown   RestartPromptDeliveryStatus = "unknown"
)

type restartAgentRelaunchOutcome struct {
	Status           RestartAgentRelaunchStatus
	Ready            bool
	ProcessAlive     bool
	ShellPID         int
	ObservationError error
}

// RestartError represents a failed restart attempt
type RestartError struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// RestartPaneOptions configures the PrintRestartPane operation
type RestartPaneOptions struct {
	Session       string         // Target session name
	Panes         []string       // Specific pane indices to restart (empty = all agents)
	Type          string         // Filter by agent type (e.g., "claude", "cc")
	All           bool           // Include all panes (including user)
	DryRun        bool           // Preview mode
	Bead          string         // Bead ID to assign after restart
	Prompt        string         // Custom prompt to send after restart (overrides --bead template)
	Config        *config.Config // Effective caller config; nil loads the merged config from disk
	ProjectDir    string         // Authoritative project directory resolved from the explicit session
	ConfigPath    string         // Selected global config used for assignment policy
	RequireConfig bool           // ConfigPath was explicitly selected and must exist
	// Model optionally overrides the relaunched agent's model using the spawn
	// variant grammar (`model` or `model@effort`). Validated against every
	// target pane's agent type before any respawn (ntm-yusj).
	Model string
	// AgentArgs are literal arguments appended after the relaunch command for
	// options the model grammar cannot express. Model/effort use Model above.
	AgentArgs string
	// Environment explicitly supplies the saved launch's omitted environment
	// for one selected agent pane. Values are runtime-only: never serialize
	// them, put them in a launch command, or infer them from ambient state.
	Environment map[string]string `json:"-"`
	// Preflight binds execution to a previous successful preview. It is used
	// by the human confirmation flow instead of resolving its selectors again.
	Preflight *RestartPanePreflight `json:"-"`
	Deps      *RestartPaneDependencies
	// Recovery is an internal, single-pane native-session recovery request.
	// It is not populated by robot flags and cannot be combined with prompts,
	// assignments, or model/argument overrides.
	Recovery *NativeSessionRecovery
}

// restartLaunchOverride is the parsed relaunch override (ntm-yusj).
type restartLaunchOverride struct {
	Model  string
	Effort string
	Args   string
}

func (o restartLaunchOverride) empty() bool {
	return o.Model == "" && o.Effort == "" && o.Args == ""
}

// restartOverrideModelPattern / restartOverrideEffortPattern mirror the spawn
// spec grammar's charsets (internal/cli modelPattern / effortPattern) so
// --restart-model rejects the same junk --cod=N:model@effort would.
var (
	restartOverrideModelPattern  = regexp.MustCompile(`^[A-Za-z0-9._/@:+-]+$`)
	restartOverrideEffortPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
)

// parseRestartLaunchOverride validates --restart-model / --restart-agent-args
// into a launch override. The model field uses the spawn variant grammar's
// model[@effort] form.
func parseRestartLaunchOverride(model, args string) (restartLaunchOverride, error) {
	override := restartLaunchOverride{Args: strings.TrimSpace(args)}
	value := strings.TrimSpace(model)
	if value == "" {
		return override, nil
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		override.Effort = strings.TrimSpace(value[at+1:])
		value = strings.TrimSpace(value[:at])
		if override.Effort == "" {
			return override, fmt.Errorf("empty reasoning effort after '@' in restart model %q (use model or model@effort)", model)
		}
		if !restartOverrideEffortPattern.MatchString(override.Effort) {
			return override, fmt.Errorf("invalid characters in reasoning effort %q; allowed: letters, numbers, . _ + -", override.Effort)
		}
	}
	if value == "" {
		return override, fmt.Errorf("empty model in restart model %q (use model or model@effort)", model)
	}
	if !restartOverrideModelPattern.MatchString(value) {
		return override, fmt.Errorf("invalid characters in restart model %q; allowed: letters, numbers, . _ / @ : + -", value)
	}
	override.Model = value
	return override, nil
}

// restartOverrideAppendFlags composes the per-agent-type flags that carry a
// model/effort override. Callers remove existing scalar options first because
// some agent CLIs reject duplicates. Agent types without a known model flag
// reject the override loudly instead of dropping it.
func restartOverrideAppendFlags(resolvedType string, override restartLaunchOverride, needModel, needEffort bool) (string, error) {
	var flags strings.Builder
	switch resolvedType {
	case "claude":
		if needModel && override.Model != "" {
			flags.WriteString(" --model " + tmux.ShellQuote(override.Model))
		}
		if needEffort && override.Effort != "" {
			flags.WriteString(" --effort " + tmux.ShellQuote(override.Effort))
		}
	case "codex":
		if needModel && override.Model != "" {
			flags.WriteString(" -m " + tmux.ShellQuote(override.Model))
		}
		if needEffort && override.Effort != "" {
			flags.WriteString(" -c model_reasoning_effort=" + tmux.ShellQuote(override.Effort))
		}
	case "gemini":
		if override.Effort != "" {
			return "", fmt.Errorf("agent type %q has no reasoning-effort flag; use a model-only override", resolvedType)
		}
		if needModel && override.Model != "" {
			flags.WriteString(" --model " + tmux.ShellQuote(override.Model))
		}
	case "grok":
		// Grok Build exposes --model and --effort (alias of --reasoning-effort);
		// last-flag-wins parsing verified against grok 1.0.5 --help (GH#251).
		if needModel && override.Model != "" {
			flags.WriteString(" --model " + tmux.ShellQuote(override.Model))
		}
		if needEffort && override.Effort != "" {
			flags.WriteString(" --effort " + tmux.ShellQuote(override.Effort))
		}
	case "omp":
		// omp takes --model (fuzzy-matched) and --thinking for the reasoning
		// level; both are last-flag-wins in `omp --help` (omp v18.2.3).
		if needModel && override.Model != "" {
			flags.WriteString(" --model " + tmux.ShellQuote(override.Model))
		}
		if needEffort && override.Effort != "" {
			flags.WriteString(" --thinking " + tmux.ShellQuote(override.Effort))
		}
	default:
		if override.Model != "" || override.Effort != "" {
			return "", fmt.Errorf("agent type %q does not support a restart model override; supported: claude, codex, gemini, grok, omp", resolvedType)
		}
	}
	return flags.String(), nil
}

// RestartPaneDependencies exposes assignment ports for focused safety tests.
// Production callers leave this nil.
type RestartPaneDependencies struct {
	LoadAssignmentPolicy   func(string, string, bool) (*config.Config, error)
	FetchActionable        func(context.Context, string, int) ([]bv.TriageRecommendation, error)
	FetchBeadDetails       func(context.Context, string, string) (*bv.BeadAssignmentDetails, error)
	AssignmentLedgerExists func(string) (bool, error)
	LoadStore              func(string) (*assignment.AssignmentStore, error)
	LoadStoreReadOnly      func(string) (*assignment.AssignmentStore, error)
	ClaimBead              func(context.Context, string, string, string) (bv.BeadClaimResult, error)
	ClaimBeadWithPolicy    func(context.Context, string, string, string, []string) (bv.BeadClaimResult, error)
	GetBeadStatus          func(context.Context, string, string) (string, error)
	NewIdempotencyKey      func() (string, error)
	ListPanes              func(context.Context, string) ([]tmux.Pane, error)
	ObserveSession         func(context.Context, string) (statuspkg.SessionObservation, error)
	DispatchDeliverer      dispatchsvc.Deliverer
	DispatchPacer          dispatchsvc.Pacer
	LoadManifest           func(string) (*resilience.SpawnManifest, error)
	PrepareLaunchCommand   func(context.Context, string, string, *resilience.LaunchBinding, string) (string, resilience.LaunchAffinity, error)
	ReadLaunchSpec         func(context.Context, string) (*tmux.AgentLaunchSpec, error)
	SetLaunchSpec          func(context.Context, string, int, tmux.AgentLaunchSpec) error
	PaneWorkingDir         func(context.Context, string) (string, error)
	// ObserveAgentProcess returns a running foreground process belonging to
	// the expected pane root, or zero while launch is still incomplete.
	ObserveAgentProcess func(context.Context, string, int) (int, error)
}

type restartBeadPreflight struct {
	Details     *bv.BeadAssignmentDetails
	Prompt      string
	Policy      *config.Config
	Store       *assignment.AssignmentStore
	Recovery    *assignment.Assignment
	Request     assignment.AtomicRequest
	Coordinator *assignment.AtomicCoordinator
	Fence       *restartAssignmentFence
}

// The assignment coordinator keeps its existing claim/ledger/dispatch protocol.
// These restart-only ports become fenced after the new process is verified;
// the shared delivery implementation receives the same opt-in tmux guard for
// every composer, prompt, Enter, and submission-rescue mutation.
type restartAssignmentFence struct {
	deps      RestartPaneDependencies
	preflight *RestartPanePreflight
	target    string
	pid       int
	spec      tmux.AgentLaunchSpec
}

func (fence *restartAssignmentFence) check(ctx context.Context) error {
	if fence.preflight == nil || fence.target == "" || fence.pid <= 0 {
		return errors.New("restart assignment has no verified replacement pane")
	}
	return fence.preflight.observeReplacement(ctx, fence.deps, fence.target, fence.pid, false, &fence.spec)
}

func (fence *restartAssignmentFence) listPanes(ctx context.Context, session string) ([]tmux.Pane, error) {
	panes, err := fence.deps.ListPanes(ctx, session)
	if err != nil || fence.preflight == nil {
		return panes, err
	}
	if session != fence.preflight.session {
		return nil, errors.New("restart assignment session changed")
	}
	if err := fence.preflight.observeReplacementPanes(ctx, fence.deps, panes, fence.target, fence.pid, false, &fence.spec); err != nil {
		return nil, err
	}
	return panes, nil
}

func (fence *restartAssignmentFence) observeSession(ctx context.Context, session string) (statuspkg.SessionObservation, error) {
	if err := fence.check(ctx); err != nil {
		return statuspkg.SessionObservation{}, err
	}
	if session != fence.preflight.session {
		return statuspkg.SessionObservation{}, errors.New("restart assignment session changed")
	}
	observation, err := fence.deps.ObserveSession(ctx, session)
	if err != nil {
		return observation, err
	}
	pane, found := observation.PaneByID(fence.target)
	if !found {
		return observation, errors.New("restart assignment replacement pane is no longer observed")
	}
	if err := fence.preflight.observeReplacementPanes(ctx, fence.deps, []tmux.Pane{pane.Metadata}, fence.target, fence.pid, false, &fence.spec); err != nil {
		return observation, err
	}
	return observation, nil
}

func (fence *restartAssignmentFence) Deliver(ctx context.Context, delivery dispatchsvc.Delivery) error {
	if delivery.Target.Ref.ID != fence.target || delivery.Target.Pane.PID != fence.pid {
		return dispatchsvc.GuaranteeNoDeliveryActuation(errors.New("restart assignment delivery target changed"))
	}
	if err := fence.check(ctx); err != nil {
		return dispatchsvc.GuaranteeNoDeliveryActuation(err)
	}
	guarded, err := tmux.WithPaneMutationGuard(ctx, fence.target, fence.pid)
	if err != nil {
		return dispatchsvc.GuaranteeNoDeliveryActuation(err)
	}
	if fence.deps.DispatchDeliverer == nil {
		return dispatchsvc.GuaranteeNoDeliveryActuation(errors.New("restart assignment delivery transport is unavailable"))
	}
	if err := fence.deps.DispatchDeliverer.Deliver(guarded, delivery); err != nil {
		return err
	}
	return fence.check(ctx)
}

type restartPromptTarget struct {
	Pane         string
	Target       string
	AgentType    tmux.AgentType
	ResolvedType string // restartPaneAgentType result: "claude", "codex", ..., "user", "unknown"
	Variant      string // Model alias (or persona name) parsed from the pane title
	Width        int    // Real pane width for width-adaptive detectors (bd-eeifh); 0 when unknown
	ExpectedPID  int    // The replacement shell this operation actually created
}

// RestartedAgentPane describes an agent pane that was successfully respawned
// and relaunched, in the shape the Agent Mail registration flow needs to
// recreate (or reuse) the pane's identity.
type RestartedAgentPane struct {
	PaneIndex int
	PaneID    string
	PaneTitle string
	AgentType string // short type ("cc", "cod", ...) when known
	Variant   string // model alias parsed from the pane title, may be empty
}

// restartPaneIdentityHook is invoked after panes are respawned so restarted
// panes regain resolvable Agent Mail identities. The cli package installs the
// gated registration flow here (robot cannot import cli without a cycle); the
// hook must be best-effort and must never fail the restart.
var restartPaneIdentityHook func(ctx context.Context, session string, panes []RestartedAgentPane)

// SetRestartPaneIdentityHook registers the post-restart identity callback.
// Passing nil disables it. The hook is shared by every restart caller
// (ntm respawn, ntm robot restart-pane, health auto-restart, diagnose).
func SetRestartPaneIdentityHook(hook func(ctx context.Context, session string, panes []RestartedAgentPane)) {
	restartPaneIdentityHook = hook
}

// notifyRestartPaneIdentityHook translates successfully restarted agent panes
// into RestartedAgentPane records and invokes the identity hook. User/unknown
// panes are skipped: they never carry an Agent Mail identity.
func notifyRestartPaneIdentityHook(ctx context.Context, session string, targets []tmux.Pane, restarted []string, multiWindow bool) {
	if restartPaneIdentityHook == nil || len(restarted) == 0 {
		return
	}
	restartedKeys := make(map[string]bool, len(restarted))
	for _, key := range restarted {
		restartedKeys[key] = true
	}
	panes := make([]RestartedAgentPane, 0, len(restarted))
	for _, pane := range targets {
		if !restartedKeys[paneTargetKey(pane, multiWindow)] {
			continue
		}
		if !restartTargetIsAgent(restartPaneAgentType(pane)) {
			continue
		}
		panes = append(panes, RestartedAgentPane{
			PaneIndex: pane.Index,
			PaneID:    pane.ID,
			PaneTitle: pane.Title,
			AgentType: string(pane.Type),
			Variant:   pane.Variant,
		})
	}
	if len(panes) == 0 {
		return
	}
	restartPaneIdentityHook(ctx, session, panes)
}

func restartPaneCancellationError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func setRestartPaneCancellation(output *RestartPaneOutput, err error, stage string) {
	if output == nil || err == nil {
		return
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = "restart canceled"
	}
	wrapped := fmt.Errorf("%s: %w", stage, err)
	if output.PromptError == "" {
		output.PromptError = wrapped.Error()
	}
	output.RobotResponse = NewErrorResponse(
		wrapped,
		ErrCodeTimeout,
		"Inspect the restarted and failed pane lists, then retry after cancellation clears",
	)
}

// GetRestartPaneContext is the cancellation-aware restart implementation.
func GetRestartPaneContext(ctx context.Context, opts RestartPaneOptions) (*RestartPaneOutput, error) {
	output := &RestartPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		RestartedAt:   time.Now().UTC(),
		Restarted:     []string{},
		Failed:        []RestartError{},
		DryRun:        opts.DryRun,
	}
	if ctx == nil {
		output.RobotResponse = NewErrorResponse(errors.New("restart-pane context is required"), ErrCodeInternalError, "Retry the command")
		return output, nil
	}
	if err := ctx.Err(); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Retry after the cancellation or timeout condition clears")
		return output, nil
	}
	deps := restartPaneDeps(opts.Deps)

	// Validate the shared N / W.P / %N grammar before touching tmux. Target
	// matching treats a malformed token as matching nothing, so without this a
	// typo'd list would silently restart only its well-formed part.
	for _, selector := range opts.Panes {
		if _, err := tmux.ParsePaneSelector(selector); err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use comma-separated N, W.P, or %N pane selectors")
			return output, nil
		}
	}
	if opts.Environment != nil {
		if len(opts.Panes) != 1 || opts.All || opts.Recovery != nil {
			output.RobotResponse = NewErrorResponse(
				errors.New("restart environment recovery requires one explicit pane selector and cannot be combined with --all or native recovery"),
				ErrCodeInvalidFlag,
				"Select one agent pane with --panes and supply its required environment using --restart-env-file",
			)
			return output, nil
		}
		if err := tmux.ValidateLaunchEnvironment(opts.Environment); err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Supply a private JSON object containing only the saved launch's required environment names")
			return output, nil
		}
	}

	exists, err := tmux.SessionExistsContext(ctx, opts.Session)
	if err != nil {
		if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
			setRestartPaneCancellation(output, cancelErr, "restart canceled while checking session")
			return output, nil
		}
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Check tmux session state")
		return output, nil
	}
	if !exists {
		output.Failed = append(output.Failed, RestartError{
			Pane:   "session",
			Reason: fmt.Sprintf("session '%s' not found", opts.Session),
		})
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use --robot-status to list available sessions",
		)
		return output, nil
	}

	panes, err := deps.ListPanes(ctx, opts.Session)
	if err != nil {
		output.Failed = append(output.Failed, RestartError{
			Pane:   "panes",
			Reason: fmt.Sprintf("failed to get panes: %v", err),
		})
		if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
			setRestartPaneCancellation(output, cancelErr, "restart canceled while reading pane topology")
			return output, nil
		}
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}

	// Build pane filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	// Topology-aware keys (#172): canonical "window.pane" on multi-window sessions.
	multiWindow := paneSessionIsMultiWindow(panes)
	targetPanes := selectRestartPaneTargets(panes, paneFilterMap, opts.Type, opts.All)
	if opts.Preflight != nil {
		targetPanes, err = opts.Preflight.selectTargets(opts.Session, panes)
		if err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodePaneNotFound, "Re-run the preview and confirm the current physical panes; no pane was respawned")
			return output, nil
		}
	}
	if opts.Environment != nil && (len(targetPanes) != 1 || !restartTargetIsAgent(restartPaneAgentType(targetPanes[0]))) {
		output.RobotResponse = NewErrorResponse(
			errors.New("restart environment recovery must resolve to exactly one agent pane"),
			ErrCodeInvalidFlag,
			"Use --panes with the intended physical pane ID or an unambiguous pane selector",
		)
		return output, nil
	}

	if len(targetPanes) == 0 {
		if opts.Recovery != nil {
			output.RobotResponse = NewErrorResponse(errors.New("native recovery target is no longer present"), ErrCodePaneNotFound, "Reobserve the exact physical pane before recovery")
		} else if strings.TrimSpace(opts.Bead) != "" {
			err := errors.New("--restart-bead requires exactly one target pane, resolved none")
			output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use --panes with one canonical agent-pane selector")
		}
		return output, nil
	}
	if err := validateRestartPaneTargets(targetPanes); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeNotImplemented, agent.GrokPhaseOneCapabilityHint)
		return output, nil
	}

	// Validate any relaunch override against every target before the first
	// respawn, so an unsupported override aborts with nothing mutated
	// (ntm-yusj).
	launchOverride, err := parseRestartLaunchOverride(opts.Model, opts.AgentArgs)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use --restart-model=model or model@effort, e.g. --restart-model=gpt-5.6-terra@high")
		return output, nil
	}
	if !launchOverride.empty() {
		for _, pane := range targetPanes {
			resolvedType := restartPaneAgentType(pane)
			if !restartTargetIsAgent(resolvedType) {
				output.RobotResponse = NewErrorResponse(
					fmt.Errorf("relaunch override cannot target non-agent pane %s (type %s)", paneTargetKey(pane, multiWindow), resolvedType),
					ErrCodeInvalidFlag,
					"Restrict --panes/--type to agent panes when using --restart-model/--restart-agent-args",
				)
				return output, nil
			}
			if _, err := restartOverrideAppendFlags(resolvedType, launchOverride, true, true); err != nil {
				output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use --restart-model only with claude, codex, or gemini panes")
				return output, nil
			}
		}
	}
	if opts.Recovery != nil {
		if len(targetPanes) != 1 || !launchOverride.empty() || strings.TrimSpace(opts.Bead) != "" || strings.TrimSpace(opts.Prompt) != "" {
			output.RobotResponse = NewErrorResponse(errors.New("native recovery requires one exact pane and cannot change assignment, prompt, model, or arguments"), ErrCodeInvalidFlag, "Use the internal recovery request without additional overrides")
			return output, nil
		}
	}

	var beadPreflight *restartBeadPreflight
	promptToSend := strings.TrimSpace(opts.Prompt)
	if beadID := strings.TrimSpace(opts.Bead); beadID != "" {
		if err := validateRestartBeadTargets(targetPanes); err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Select exactly one supported agent pane")
			return output, nil
		}
		beadPreflight, err = preflightRestartBead(ctx, opts, targetPanes[0], deps)
		if err != nil {
			errorCode := ErrCodeInvalidFlag
			hint := "Fix assignment policy, bv plan integrity, or live Beads eligibility before retrying"
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				errorCode = ErrCodeTimeout
				hint = "Retry after the cancellation or timeout condition clears"
			}
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("authorize restart assignment for %s: %w", beadID, err),
				errorCode,
				hint,
			)
			return output, nil
		}
		promptToSend = beadPreflight.Prompt
		output.BeadAssigned = beadID
	}
	if err := ctx.Err(); err != nil {
		setRestartPaneCancellation(output, err, "restart canceled after assignment preflight")
		return output, nil
	}

	restartCfg := opts.Config
	if beadPreflight != nil && beadPreflight.Policy != nil {
		restartCfg = beadPreflight.Policy
	}
	if restartCfg == nil {
		var cfgErr error
		restartCfg, cfgErr = config.LoadMerged(mustGetwd(), config.DefaultPath())
		if cfgErr != nil {
			restartCfg = config.Default()
		}
	}
	launchPlan, err := prepareRestartLaunchPlan(ctx, opts.Session, targetPanes, multiWindow, restartCfg, launchOverride, deps, opts.Environment)
	defer launchPlan.close()
	if err == nil && opts.Preflight != nil {
		err = opts.Preflight.compare(launchPlan.Preflight)
	}
	if err == nil && opts.Recovery != nil {
		err = prepareNativeRecoveryLaunchPlan(ctx, targetPanes[0], multiWindow, restartCfg, opts.Recovery, &launchPlan)
	}
	if err == nil {
		err = launchPlan.prepareDelivery(ctx, opts.Session, opts.Environment)
	}
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Supply omitted environment names with --restart-env-file and one --panes target, or restore the named launcher profile or saved launch dependencies; no pane was respawned",
		)
		return output, nil
	}
	output.LaunchAffinity = launchPlan.Affinity
	if err := launchPlan.Preflight.observe(ctx, deps); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodePaneNotFound, "Restart inputs changed during preflight; inspect the physical panes before retrying; no pane was respawned")
		return output, nil
	}
	output.Preflight = launchPlan.Preflight

	// Dry-run mode
	if opts.DryRun {
		output.DryRun = true
		for _, pane := range targetPanes {
			paneKey := paneTargetKey(pane, multiWindow)
			output.WouldAffect = append(output.WouldAffect, paneKey)
		}
		return output, nil
	}
	if beadPreflight != nil && restartAssignmentWasSent(beadPreflight.Recovery) {
		applyRestartAssignmentReplay(output, beadPreflight.Recovery)
		return output, nil
	}
	if opts.Recovery != nil {
		if err := opts.Recovery.beforeRespawn(ctx, opts.Session, deps); err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Native recovery was stopped before respawning the pane; inspect the account activation outcome")
			return output, nil
		}
	}

	// Restart targets — track pane IDs for post-restart relaunch/liveness steps.
	// The helper repeats the batch preflight defensively so no future caller can
	// accidentally move validation after the first respawn.
	if err := launchPlan.Preflight.observe(ctx, deps); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodePaneNotFound, "Restart inputs changed before respawn; no pane was respawned")
		return output, nil
	}
	respawnPane := func(respawnCtx context.Context, target string, kill bool) error {
		expected, err := launchPlan.Preflight.target(target)
		if err != nil {
			return err
		}
		if err := expected.observeBeforeRespawn(respawnCtx, opts.Session, deps); err != nil {
			return err
		}
		return tmux.DefaultClient.RespawnPaneIfCurrentContext(respawnCtx, target, expected.pane.PID, kill, expected.workingDir, restartTargetIsAgent(restartPaneAgentType(expected.pane)))
	}
	restartedPaneInfo, err := respawnRestartPaneTargetsContext(
		ctx,
		targetPanes,
		multiWindow,
		output,
		respawnPane,
		paneShellPIDContext,
	)
	if err != nil {
		if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
			setRestartPaneCancellation(output, cancelErr, "restart canceled during pane respawn")
			return output, nil
		}
		output.RobotResponse = NewErrorResponse(err, ErrCodeNotImplemented, agent.GrokPhaseOneCapabilityHint)
		return output, nil
	}

	// Relaunch agent CLIs in respawned agent panes (#187). respawn-pane -k
	// only restores the pane's default command — the login shell. In ntm
	// sessions the agent CLI is launched by keystroke after spawn, so without
	// an explicit relaunch the pane is left at a bare shell and any restart
	// prompt would be typed into zsh instead of an agent.
	agentPaneReady := make(map[string]bool, len(output.Restarted))
	if len(output.Restarted) > 0 {
		// Let fresh shells initialize before typing into them.
		if err := waitForRestartPaneDelay(ctx, restartPaneSettleDelay); err != nil {
			appendRestartCancellationFailures(output, output.Restarted, 0, "agent relaunch canceled before shell settle completed", err)
			setRestartPaneCancellation(output, err, "restart canceled while fresh panes settled")
			return output, nil
		}

		// Restore Agent Mail identities BEFORE any agent CLI is launched
		// (ntm#321). The respawn replaced each pane's process, so this
		// re-runs the same gated registration flow spawn uses: it reuses
		// existing identities from the session registry (#69), refreshes the
		// server's pane binding, persists any rotated registration token, and
		// rewrites the identity files. Running it after the relaunch loop
		// meant the agent had already booted and resolved (or failed to
		// resolve) its identity — and had already inherited the previous
		// credential — before any of that landed. Best-effort by
		// construction: the hook must never fail the restart, and it is
		// invoked exactly once per restart.
		verifiedRestarts := make([]string, 0, len(output.Restarted))
		verifiedTargets := make(map[string]bool, len(output.Restarted))
		for _, paneKey := range output.Restarted {
			pids := output.PaneShellPIDs[paneKey]
			if pids.After > 0 && pids.After != pids.Before {
				info := restartedPaneInfo[paneKey]
				if restartTargetIsAgent(info.ResolvedType) {
					if err := launchPlan.Preflight.observeReplacement(ctx, deps, info.Target, pids.After, true, nil); err != nil {
						appendRestartFailureOnce(output, paneKey, fmt.Sprintf("agent relaunch withheld: %v", err))
						continue
					}
				}
				verifiedRestarts = append(verifiedRestarts, paneKey)
				verifiedTargets[paneKey] = true
			}
		}
		notifyRestartPaneIdentityHook(ctx, opts.Session, targetPanes, verifiedRestarts, multiWindow)

		output.AgentRelaunched = make(map[string]bool)
		output.AgentRelaunchStatus = make(map[string]RestartAgentRelaunchStatus)
		output.ProcessAlive = make(map[string]bool, len(output.Restarted))
		for paneIndex, paneKey := range output.Restarted {
			info := restartedPaneInfo[paneKey]

			if !restartTargetIsAgent(info.ResolvedType) {
				// User/unknown panes have no agent CLI to relaunch; the fresh
				// shell is the fully restored state.
				pid, pidErr := paneShellPIDContext(ctx, info.Target)
				if cancelErr := restartPaneCancellationError(ctx, pidErr); cancelErr != nil {
					output.Failed = append(output.Failed, RestartError{Pane: paneKey, Reason: cancelErr.Error()})
					appendRestartCancellationFailures(output, output.Restarted, paneIndex+1, "agent relaunch skipped", cancelErr)
					setRestartPaneCancellation(output, cancelErr, fmt.Sprintf("restart canceled while checking pane %s", paneKey))
					return output, nil
				}
				output.ProcessAlive[paneKey] = pid > 0 && process.IsAlive(pid)
				continue
			}

			launchCmd, ok := launchPlan.Commands[paneKey]
			if !ok {
				appendRestartFailureOnce(output, paneKey, "missing preflighted relaunch command")
				output.AgentRelaunched[paneKey] = false
				output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchFailed
				continue
			}
			pids := output.PaneShellPIDs[paneKey]
			if pids.After <= 0 || pids.After == pids.Before {
				appendRestartFailureOnce(output, paneKey, "agent relaunch withheld: replacement pane PID was not verified; saved launch specification was left unchanged")
				output.AgentRelaunched[paneKey] = false
				output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchUnknown
				continue
			}
			if !verifiedTargets[paneKey] {
				output.AgentRelaunched[paneKey] = false
				output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchUnknown
				continue
			}
			if replay := launchPlan.Replay[paneKey]; replay != nil {
				launchCmd, err = replay.Prepare(ctx, launchPlan.Directories[paneKey], opts.Session, launchPlan.Indices[paneKey])
				if err == nil {
					launchCmd, err = tmux.BuildPaneCommand(launchPlan.Directories[paneKey], launchCmd)
				}
				if err != nil {
					appendRestartFailureOnce(output, paneKey, fmt.Sprintf("prepare saved launch after respawn: %v", err))
					output.AgentRelaunched[paneKey] = false
					output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchFailed
					continue
				}
			}
			if expected, exists := launchPlan.DeliveryCommands[paneKey]; !exists || expected != launchCmd {
				appendRestartFailureOnce(output, paneKey, "saved launch preparation changed after delivery preflight; launch was withheld")
				output.AgentRelaunched[paneKey] = false
				output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchFailed
				continue
			}
			// Respawn preserves pane options. Refresh the saved command only
			// after it has replaced the old process, before delivering the new
			// launch, so a failed respawn cannot mislabel a still-running agent.
			if err := launchPlan.Preflight.observeReplacement(ctx, deps, info.Target, pids.After, true, nil); err != nil {
				appendRestartFailureOnce(output, paneKey, fmt.Sprintf("agent relaunch withheld: %v", err))
				output.AgentRelaunched[paneKey] = false
				output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchUnknown
				continue
			}
			if err := deps.SetLaunchSpec(ctx, info.Target, pids.After, launchPlan.Specs[paneKey]); err != nil {
				appendRestartFailureOnce(output, paneKey, fmt.Sprintf("record launch specification after respawn: %v", err))
				output.AgentRelaunched[paneKey] = false
				output.AgentRelaunchStatus[paneKey] = RestartAgentRelaunchFailed
				continue
			}
			sendLaunch := func(sendCtx context.Context, target, command string, enter bool, agentType tmux.AgentType) error {
				spec := launchPlan.Specs[paneKey]
				if err := launchPlan.Preflight.observeReplacement(sendCtx, deps, target, pids.After, true, &spec); err != nil {
					return err
				}
				if handoff := launchPlan.handoffs[paneKey]; handoff != nil {
					return handoff.SendIfCurrent(sendCtx, target, command, enter, agentType, pids.After)
				}
				return tmux.DefaultClient.SendKeysForAgentIfCurrentContext(sendCtx, target, command, enter, agentType, pids.After, true)
			}
			setAgentType := func(setCtx context.Context, target string, agentType tmux.AgentType) error {
				return tmux.DefaultClient.SetPaneAgentTypeIfCurrentContext(setCtx, target, pids.After, agentType)
			}
			outcome, phase, lifecycleErr := relaunchRestartPaneAgentContext(
				ctx,
				info,
				launchCmd,
				restartPaneReadyTimeout,
				setAgentType,
				sendLaunch,
				paneShellPIDContext,
				func(readyCtx context.Context, target string, rootPID int, kind string, timeout time.Duration) (bool, error) {
					return waitForPaneAgentReadyWithContext(readyCtx, target, rootPID, kind, timeout,
						restartPaneReadyPollInterval, tmux.CapturePaneVisibleContext, deps.ObserveAgentProcess)
				},
				deps.ObserveAgentProcess,
			)
			if lifecycleErr == nil && outcome.Ready {
				spec := launchPlan.Specs[paneKey]
				if identityErr := launchPlan.Preflight.observeReplacement(ctx, deps, info.Target, pids.After, false, &spec); identityErr != nil {
					outcome.Ready = false
					outcome.ProcessAlive = false
					outcome.Status = RestartAgentRelaunchUnknown
					phase, lifecycleErr = "identity verification", identityErr
				}
			}
			agentPaneReady[paneKey] = outcome.Ready
			output.AgentRelaunched[paneKey] = outcome.Ready
			output.AgentRelaunchStatus[paneKey] = outcome.Status
			output.ProcessAlive[paneKey] = outcome.ProcessAlive
			if lifecycleErr != nil {
				reason := formatRestartAgentLifecycleError(phase, lifecycleErr, outcome)
				appendRestartFailureOnce(output, paneKey, reason)
				if cancelErr := restartPaneCancellationError(ctx, lifecycleErr); cancelErr != nil {
					appendRestartCancellationFailures(output, output.Restarted, paneIndex+1, "agent relaunch skipped", cancelErr)
					setRestartPaneCancellation(output, cancelErr, fmt.Sprintf("restart canceled during pane %s agent %s", paneKey, phase))
					return output, nil
				}
				continue
			}
			if !outcome.Ready {
				output.Failed = append(output.Failed, RestartError{
					Pane:   paneKey,
					Reason: fmt.Sprintf("agent not ready within %s after relaunch", restartPaneReadyTimeout),
				})
			}
		}
	}

	// Bead prompts cross the shared atomic claim-ledger-dispatch boundary.
	// Ordinary restart prompts retain the direct best-effort behavior.
	if err := ctx.Err(); err != nil {
		if beadPreflight != nil || promptToSend != "" {
			appendRestartCancellationFailures(output, output.Restarted, 0, "prompt delivery skipped", err)
		}
		if beadPreflight == nil && promptToSend != "" {
			output.PromptDelivery = make(map[string]RestartPromptDeliveryStatus, len(output.Restarted))
			for _, paneKey := range output.Restarted {
				output.PromptDelivery[paneKey] = RestartPromptSkipped
			}
		}
		setRestartPaneCancellation(output, err, "restart canceled before prompt delivery")
		return output, nil
	}
	if beadPreflight != nil && len(output.Restarted) > 0 {
		paneKey := output.Restarted[0]
		info := restartedPaneInfo[paneKey]
		if restartTargetIsAgent(info.ResolvedType) && !agentPaneReady[paneKey] {
			output.PromptError = fmt.Sprintf("pane %s: agent not ready, assignment not started", paneKey)
		} else {
			fence := beadPreflight.Fence
			var result assignment.AtomicResult
			var executeErr error
			if fence == nil {
				executeErr = errors.New("restart assignment replacement fence is unavailable")
			} else {
				fence.preflight, fence.target, fence.pid, fence.spec = launchPlan.Preflight, info.Target, info.ExpectedPID, launchPlan.Specs[paneKey]
				if executeErr = fence.check(ctx); executeErr == nil {
					result, executeErr = executeRestartBeadAfterSafeObservation(
						ctx,
						opts.Session,
						beadPreflight.Request,
						restartPaneDispatchReadyTimeout,
						restartPaneReadyPollInterval,
						fence.observeSession,
						func(executeCtx context.Context, request assignment.AtomicRequest) (assignment.AtomicResult, error) {
							if err := fence.check(executeCtx); err != nil {
								return assignment.AtomicResult{}, err
							}
							return beadPreflight.Coordinator.Execute(executeCtx, request)
						},
					)
				}
			}
			if cancelErr := restartPaneCancellationError(ctx, executeErr); cancelErr != nil {
				appendRestartFailureOnce(output, paneKey, fmt.Sprintf("atomic assignment canceled: %v", cancelErr))
			}
			applyRestartAtomicResult(output, result, executeErr)
		}
	} else if promptToSend != "" && len(output.Restarted) > 0 {
		promptTargets := make([]restartPromptTarget, 0, len(output.Restarted))
		var promptErrors []string
		output.PromptDelivery = make(map[string]RestartPromptDeliveryStatus, len(output.Restarted))
		for _, paneKey := range output.Restarted {
			info := restartedPaneInfo[paneKey]
			if restartTargetIsAgent(info.ResolvedType) && !agentPaneReady[paneKey] {
				promptErrors = append(promptErrors, fmt.Sprintf("pane %s: agent not ready, prompt not sent", paneKey))
				output.PromptDelivery[paneKey] = RestartPromptSkipped
				continue
			}
			promptTargets = append(promptTargets, info)
		}
		// Delivery-readiness gate + post-send submission verification
		// (bd-rf0ka). Both apply only to agent panes: user/unknown panes have
		// no composer and their shell IS the restored foreground.
		gate := func(gateCtx context.Context, target restartPromptTarget) (bool, string, error) {
			var published *tmux.AgentLaunchSpec
			if spec, exists := launchPlan.Specs[target.Pane]; exists {
				published = &spec
			}
			if err := launchPlan.Preflight.observeReplacement(gateCtx, deps, target.Target, target.ExpectedPID, false, published); err != nil {
				return false, err.Error(), err
			}
			if !restartTargetIsAgent(target.ResolvedType) {
				return true, "", nil
			}
			return waitForRestartPromptDeliveryReadyContext(
				gateCtx,
				target,
				restartPaneReadyTimeout,
				restartPaneReadyPollInterval,
				paneCurrentCommandContext,
				tmux.ComposerReadyForDelivery,
			)
		}
		verify := func(verifyCtx context.Context, target restartPromptTarget) error {
			var published *tmux.AgentLaunchSpec
			if spec, exists := launchPlan.Specs[target.Pane]; exists {
				published = &spec
			}
			if err := launchPlan.Preflight.observeReplacement(verifyCtx, deps, target.Target, target.ExpectedPID, false, published); err != nil {
				return err
			}
			if !restartTargetIsAgent(target.ResolvedType) {
				return nil
			}
			guarded, err := tmux.WithPaneMutationGuard(verifyCtx, target.Target, target.ExpectedPID)
			if err != nil {
				return err
			}
			if err := dispatchsvc.VerifyAgentSubmission(guarded, target.Target, promptToSend, target.AgentType, target.Width); err != nil {
				return err
			}
			return launchPlan.Preflight.observeReplacement(verifyCtx, deps, target.Target, target.ExpectedPID, false, published)
		}
		sendPrompt := func(sendCtx context.Context, target, prompt string, agentType tmux.AgentType) error {
			for _, info := range promptTargets {
				if info.Target != target {
					continue
				}
				var published *tmux.AgentLaunchSpec
				if spec, exists := launchPlan.Specs[info.Pane]; exists {
					published = &spec
				}
				if err := launchPlan.Preflight.observeReplacement(sendCtx, deps, target, info.ExpectedPID, false, published); err != nil {
					return err
				}
				return tmux.DefaultClient.SendKeysForAgentDoubleEnterIfCurrentContext(sendCtx, target, prompt, agentType, info.ExpectedPID)
			}
			return errors.New("restart prompt target is not a verified replacement pane")
		}
		report, deliveryErr := sendRestartPromptsContext(
			ctx,
			promptTargets,
			promptToSend,
			sendPrompt,
			gate,
			verify,
		)
		promptErrors = append(promptErrors, report.Errors...)
		for paneKey, status := range report.Status {
			output.PromptDelivery[paneKey] = status
		}
		for _, failure := range report.PaneFailures {
			appendRestartFailureOnce(output, failure.Pane, failure.Reason)
		}

		if len(promptErrors) > 0 {
			setRestartPanePromptFailure(output, promptErrors)
			if report.Withheld {
				output.ErrorCode = ErrCodeRestartPromptNotDelivered
				output.Hint = "The restart completed, but the prompt was withheld because the pane never became ready for typed input; no keystrokes were sent to withheld panes. Inspect prompt_delivery and re-send with --robot-send once the agent is up."
			}
		} else {
			output.PromptSent = len(promptTargets) > 0
		}
		if cancelErr := restartPaneCancellationError(ctx, deliveryErr); cancelErr != nil {
			for _, paneKey := range report.CanceledPanes {
				appendRestartFailureOnce(output, paneKey, fmt.Sprintf("prompt delivery canceled: %v", cancelErr))
			}
			setRestartPaneCancellation(output, cancelErr, "restart canceled during prompt delivery")
			return output, nil
		}
	}

	// Honest overall status (#187): any per-pane failure (respawn, relaunch,
	// or readiness) degrades overall success instead of reporting success:true.
	if len(output.Failed) > 0 {
		output.Success = false
		if output.Error == "" {
			output.Error = fmt.Sprintf("%d pane(s) failed to restart cleanly", len(output.Failed))
			output.ErrorCode = ErrCodeInternalError
		}
	}

	return output, nil
}

func setRestartPanePromptFailure(output *RestartPaneOutput, promptErrors []string) {
	if output == nil || len(promptErrors) == 0 {
		return
	}

	output.PromptSent = false
	output.PromptError = strings.Join(promptErrors, "; ")
	output.Success = false
	output.ErrorCode = ErrCodePromptSendFailed
	output.Error = fmt.Sprintf("%d restarted pane(s) did not receive the requested prompt", len(promptErrors))
	output.Hint = "The restart completed, but prompt delivery was not confirmed. Inspect prompt_delivery and prompt_error before retrying."
}

func restartPaneDeps(custom *RestartPaneDependencies) RestartPaneDependencies {
	observer := statuspkg.NewSessionObserver(statuspkg.NewDetector())
	deps := RestartPaneDependencies{
		LoadAssignmentPolicy:   loadAuthoritativeAssignmentPolicy,
		FetchActionable:        getAssignableActionableRecommendations,
		FetchBeadDetails:       bv.GetBeadAssignmentDetailsContext,
		AssignmentLedgerExists: restartPaneAssignmentLedgerExists,
		LoadStore:              assignment.LoadStoreStrict,
		LoadStoreReadOnly:      assignment.LoadStoreStrictReadOnly,
		ClaimBead:              bv.ClaimBeadForAssignment,
		ClaimBeadWithPolicy:    bv.ClaimBeadForAssignmentWithOperatorGatedLabels,
		GetBeadStatus:          bv.GetBeadStatusContext,
		NewIdempotencyKey:      assignment.NewAssignmentIdempotencyKey,
		ListPanes:              tmux.GetPanesContext,
		ObserveSession:         observer.Observe,
		DispatchDeliverer:      dispatchsvc.TMUXDeliverer{},
		LoadManifest:           resilience.LoadManifest,
		PrepareLaunchCommand:   resilience.PrepareLaunchCommand,
		ReadLaunchSpec:         tmux.ReadPaneLaunchSpecContext,
		SetLaunchSpec:          tmux.DefaultClient.SetPaneLaunchSpecIfCurrentContext,
		PaneWorkingDir:         restartPaneWorkingDirectory,
		ObserveAgentProcess:    observeRestartForegroundProcess,
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
	if custom.FetchBeadDetails != nil {
		deps.FetchBeadDetails = custom.FetchBeadDetails
	}
	if custom.AssignmentLedgerExists != nil {
		deps.AssignmentLedgerExists = custom.AssignmentLedgerExists
	}
	if custom.LoadStore != nil {
		deps.LoadStore = custom.LoadStore
	}
	if custom.LoadStoreReadOnly != nil {
		deps.LoadStoreReadOnly = custom.LoadStoreReadOnly
	}
	if custom.ClaimBead != nil {
		deps.ClaimBead = custom.ClaimBead
		deps.ClaimBeadWithPolicy = nil
	}
	if custom.ClaimBeadWithPolicy != nil {
		deps.ClaimBeadWithPolicy = custom.ClaimBeadWithPolicy
	}
	if custom.GetBeadStatus != nil {
		deps.GetBeadStatus = custom.GetBeadStatus
	}
	if custom.NewIdempotencyKey != nil {
		deps.NewIdempotencyKey = custom.NewIdempotencyKey
	}
	if custom.ListPanes != nil {
		deps.ListPanes = custom.ListPanes
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
	if custom.LoadManifest != nil {
		deps.LoadManifest = custom.LoadManifest
	}
	if custom.PrepareLaunchCommand != nil {
		deps.PrepareLaunchCommand = custom.PrepareLaunchCommand
	}
	if custom.ReadLaunchSpec != nil {
		deps.ReadLaunchSpec = custom.ReadLaunchSpec
	}
	if custom.SetLaunchSpec != nil {
		deps.SetLaunchSpec = custom.SetLaunchSpec
	}
	if custom.PaneWorkingDir != nil {
		deps.PaneWorkingDir = custom.PaneWorkingDir
	}
	if custom.ObserveAgentProcess != nil {
		deps.ObserveAgentProcess = custom.ObserveAgentProcess
	}
	return deps
}

func restartPaneAssignmentLedgerExists(session string) (bool, error) {
	path := filepath.Join(assignment.StorageDir(), session, "assignments.json")
	for _, candidate := range []string{path, path + ".bak"} {
		info, err := os.Stat(candidate)
		switch {
		case err == nil && !info.Mode().IsRegular():
			return false, fmt.Errorf("assignment ledger %s is not a regular file", candidate)
		case err == nil:
			return true, nil
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return false, fmt.Errorf("inspect assignment ledger %s: %w", candidate, err)
		}
	}
	return false, nil
}

func preflightRestartBead(
	ctx context.Context,
	opts RestartPaneOptions,
	pane tmux.Pane,
	deps RestartPaneDependencies,
) (*restartBeadPreflight, error) {
	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		return nil, errors.New("authoritative project directory is required for --restart-bead")
	}
	beadID := strings.TrimSpace(opts.Bead)
	if beadID == "" {
		return nil, errors.New("bead ID is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	policy, err := deps.LoadAssignmentPolicy(projectDir, opts.ConfigPath, opts.RequireConfig)
	if err != nil {
		return nil, fmt.Errorf("load assignment safety policy: %w", err)
	}
	operatorGatedLabels := []string(nil)
	if policy != nil {
		operatorGatedLabels = append(operatorGatedLabels, policy.Assign.OperatorGatedLabels...)
	}
	actionable, err := readRobotAssignmentPreflight(ctx, projectDir, opts.DryRun, func() ([]bv.TriageRecommendation, error) {
		return deps.FetchActionable(ctx, projectDir, 0)
	})
	if err != nil {
		return nil, fmt.Errorf("verify actionable bv plan: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	details, err := readRobotAssignmentPreflight(ctx, projectDir, opts.DryRun, func() (*bv.BeadAssignmentDetails, error) {
		return deps.FetchBeadDetails(ctx, projectDir, beadID)
	})
	if err != nil {
		return nil, fmt.Errorf("read live Beads eligibility: %w", err)
	}
	if details == nil || strings.TrimSpace(details.ID) != beadID {
		return nil, fmt.Errorf("live Beads details do not identify %s", beadID)
	}
	if strings.TrimSpace(details.Title) == "" {
		return nil, fmt.Errorf("bead %s has an empty title", beadID)
	}

	agentType := restartPaneAgentType(pane)
	target := strings.TrimSpace(pane.ID)
	if target == "" {
		target = pane.Ref().Physical()
	}
	agentName := restartPaneAssignmentActor(opts.Session, target)
	prompt := strings.TrimSpace(opts.Prompt)
	if prompt == "" {
		prompt = buildRestartBeadPrompt(beadID, details.Title)
	}

	planErr := validateRestartActionablePlanWithPolicy(beadID, actionable, operatorGatedLabels)
	freshDetailsErr := validateRestartFreshDetailsWithPolicy(details, time.Now(), operatorGatedLabels)
	freshAuthorized := planErr == nil && freshDetailsErr == nil
	possibleRecovery := strings.EqualFold(strings.TrimSpace(details.Status), "in_progress") && strings.TrimSpace(details.Assignee) != ""
	if !freshAuthorized && !possibleRecovery {
		if planErr != nil {
			return nil, planErr
		}
		return nil, freshDetailsErr
	}
	ledgerExists, err := deps.AssignmentLedgerExists(opts.Session)
	if err != nil {
		return nil, fmt.Errorf("inspect assignment ledger: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var store *assignment.AssignmentStore
	if ledgerExists {
		loadStore := deps.LoadStore
		if opts.DryRun {
			loadStore = deps.LoadStoreReadOnly
		}
		store, err = loadStore(opts.Session)
		if err != nil {
			return nil, fmt.Errorf("load assignment ledger: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateRestartStorePreflight(store, beadID, target); err != nil {
			return nil, err
		}
	}

	var recovery *assignment.Assignment
	if store != nil {
		existing := store.Get(beadID)
		if existing != nil && !robotAtomicAssignmentTerminal(existing.Status) {
			recovery = restartMatchingAssignment(existing, target, pane.Index, agentType, agentName, prompt)
			if recovery == nil {
				return nil, fmt.Errorf("bead %s already has a different active assignment intent", beadID)
			}
		}
	}
	if recovery != nil {
		if err := validateRestartRecoveryDetailsWithPolicy(details, recovery, time.Now(), operatorGatedLabels); err != nil {
			return nil, err
		}
		if recovery.DispatchState == assignment.DispatchSending {
			return nil, assignment.ErrDispatchOutcomeUnknown
		}
	} else if !freshAuthorized {
		if planErr != nil {
			return nil, planErr
		}
		return nil, freshDetailsErr
	}

	// The same read-only dispatch checks must authorize both preview and
	// execution. In particular, a blocked title or prompt must not make a
	// dry run promise a restart that execution refuses. These checks need no
	// durable assignment identity, ledger creation, claim, or delivery.
	redactionConfig := config.Default().Redaction.ToRedactionLibConfig()
	if policy != nil {
		redactionConfig = policy.Redaction.ToRedactionLibConfig()
	}
	fence := &restartAssignmentFence{deps: deps}
	dispatchPort := newRobotAtomicPaneDispatchPort(
		opts.Session, fence.listPanes, fence.observeSession, redactionConfig, fence, deps.DispatchPacer,
	)
	if _, err := dispatchPort.Preflight(ctx, assignment.DispatchRequest{
		BeadID: beadID, BeadTitle: details.Title, Target: target, Pane: pane.Index,
		AgentType: agentType, AgentName: agentName, Prompt: prompt,
	}); err != nil {
		return nil, fmt.Errorf("preflight restart assignment prompt: %w", err)
	}

	if opts.DryRun {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &restartBeadPreflight{Details: details, Prompt: prompt, Policy: policy, Store: store, Recovery: recovery}, nil
	}
	if store == nil {
		store, err = deps.LoadStore(opts.Session)
		if err != nil {
			return nil, fmt.Errorf("load assignment ledger: %w", err)
		}
		if err := validateRestartStorePreflight(store, beadID, target); err != nil {
			return nil, err
		}
		if existing := store.Get(beadID); existing != nil && !robotAtomicAssignmentTerminal(existing.Status) {
			recovery = restartMatchingAssignment(existing, target, pane.Index, agentType, agentName, prompt)
			if recovery == nil {
				return nil, fmt.Errorf("bead %s acquired a different active assignment intent during preflight", beadID)
			}
			if err := validateRestartRecoveryDetailsWithPolicy(details, recovery, time.Now(), operatorGatedLabels); err != nil {
				return nil, err
			}
		}
	}

	key, err := robotAtomicIdempotencyKey(store, beadID, target, pane.Index, agentType, agentName, prompt, false, nil, deps.NewIdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("resolve restart assignment identity: %w", err)
	}
	request := assignment.AtomicRequest{
		BeadID: beadID, BeadTitle: details.Title, Target: target, OccupancyKey: target, Pane: pane.Index,
		AgentType: agentType, AgentName: agentName, Actor: agentName, Prompt: prompt, IdempotencyKey: key,
	}
	if recovery != nil {
		request.RecoveredIntentSHA256 = strings.TrimSpace(recovery.IntentSHA256)
		if request.RecoveredIntentSHA256 == "" {
			request.RecoveredIntentSHA256 = strings.TrimSpace(recovery.PromptSHA256)
		}
	}
	claimBead := deps.ClaimBead
	if deps.ClaimBeadWithPolicy != nil {
		claimBead = func(claimCtx context.Context, dir, id, actor string) (bv.BeadClaimResult, error) {
			return deps.ClaimBeadWithPolicy(claimCtx, dir, id, actor, operatorGatedLabels)
		}
	}
	coordinator := assignment.NewAtomicCoordinator(
		store, newRobotAtomicClaimPort(projectDir, claimBead), nil, dispatchPort, dispatchPort,
	).WithWorkItemStatusPort(assignment.WorkItemStatusFunc(func(statusCtx context.Context, id string) (string, error) {
		return deps.GetBeadStatus(statusCtx, projectDir, id)
	})).WithAssignmentEligibilityAuthorizationPort(
		newRestartAssignmentEligibilityPort(projectDir, operatorGatedLabels, deps.FetchBeadDetails),
	)
	return &restartBeadPreflight{
		Details: details, Prompt: prompt, Policy: policy, Store: store, Recovery: recovery,
		Request: request, Coordinator: coordinator, Fence: fence,
	}, nil
}

func restartPaneAssignmentActor(session, target string) string {
	return fmt.Sprintf("%s-pane-%s", strings.TrimSpace(session), strings.TrimPrefix(strings.TrimSpace(target), "%"))
}

func newRestartAssignmentEligibilityPort(
	projectDir string,
	operatorGatedLabels []string,
	fetch func(context.Context, string, string) (*bv.BeadAssignmentDetails, error),
) assignment.AssignmentEligibilityAuthorizationPort {
	operatorGatedLabels = append([]string(nil), operatorGatedLabels...)
	return assignment.AssignmentEligibilityAuthorizationFunc(func(ctx context.Context, request assignment.AssignmentEligibilityAuthorizationRequest) error {
		details, err := fetch(ctx, projectDir, request.BeadID)
		if err != nil {
			return fmt.Errorf("read final live Beads eligibility: %w", err)
		}
		err = bv.ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, bv.BeadAssignmentAuthorization{
			BeadID: request.BeadID, ExpectedAssignee: request.ClaimActor,
			AllowUnassignedOpen:  request.AllowUnassignedOpen,
			AllowOwnedOpen:       request.AllowOwnedOpen,
			AllowOwnedInProgress: request.AllowOwnedInProgress,
		}, operatorGatedLabels)
		if errors.Is(err, bv.ErrBeadAssignmentIneligible) {
			return fmt.Errorf("%w: %v", assignment.ErrClaimIneligible, err)
		}
		return err
	})
}

func validateRestartStorePreflight(store *assignment.AssignmentStore, beadID, target string) error {
	if store == nil {
		return errors.New("assignment ledger is required")
	}
	for _, current := range store.ListActive() {
		if current == nil || current.BeadID == beadID {
			continue
		}
		identity, err := assignment.CanonicalPaneIdentity(current)
		if err != nil {
			return fmt.Errorf("verify active assignment %s occupancy: %w", current.BeadID, err)
		}
		if identity == target {
			return fmt.Errorf("target %s is already occupied by bead %s", target, current.BeadID)
		}
	}
	prior := store.Get(beadID)
	if prior == nil {
		return nil
	}
	if prior.ClearState != assignment.ClearStateNone {
		return fmt.Errorf("assignment %s is awaiting reservation release", beadID)
	}
	if robotAtomicAssignmentTerminal(prior.Status) {
		if strings.TrimSpace(prior.PendingCompletionEventID) != "" {
			return fmt.Errorf("assignment %s has unacknowledged completion event %s", beadID, prior.PendingCompletionEventID)
		}
		if restartAssignmentHasReservationEvidence(prior) {
			return fmt.Errorf("assignment %s still owns reservation receipts", beadID)
		}
	}
	return nil
}

func restartAssignmentHasReservationEvidence(current *assignment.Assignment) bool {
	if current == nil {
		return false
	}
	if current.ClearState != assignment.ClearStateNone || len(current.ReservationIDs) > 0 || len(current.ReservedPaths) > 0 {
		return true
	}

	state := current.ReservationState
	if state == "" {
		if current.ReservationCompleted {
			state = assignment.ReservationReserved
		} else {
			state = assignment.ReservationPending
		}
	}
	switch state {
	case assignment.ReservationReserving, assignment.ReservationUnknown:
		return true
	case assignment.ReservationReserved:
		return !current.ReservationCompleted || strings.TrimSpace(current.ReservationError) != ""
	default:
		// ReservationRequired is durable policy, not evidence of a live lease.
		// In particular, a terminal Released record with no handles is clean.
		return false
	}
}

func restartMatchingAssignment(
	existing *assignment.Assignment,
	target string,
	pane int,
	agentType, agentName, prompt string,
) *assignment.Assignment {
	if existing == nil || existing.Pane != pane || strings.TrimSpace(existing.DispatchTarget) != target ||
		normalizeAgentType(existing.AgentType) != normalizeAgentType(agentType) ||
		strings.TrimSpace(existing.AgentName) != agentName || restartAssignmentHasReservationEvidence(existing) {
		return nil
	}
	occupancy := strings.TrimSpace(existing.OccupancyKey)
	if occupancy == "" {
		occupancy = strings.TrimSpace(existing.DispatchTarget)
	}
	if occupancy != target {
		return nil
	}
	intent := strings.TrimSpace(existing.IntentSHA256)
	if intent == "" {
		intent = strings.TrimSpace(existing.PromptSHA256)
	}
	if intent == "" || intent != assignment.PromptSHA256(prompt) || strings.TrimSpace(existing.IdempotencyKey) == "" {
		return nil
	}
	return existing
}

func validateRestartActionablePlanWithPolicy(beadID string, actionable []bv.TriageRecommendation, operatorGatedLabels []string) error {
	var match *bv.TriageRecommendation
	for i := range actionable {
		if strings.TrimSpace(actionable[i].ID) != beadID {
			continue
		}
		if match != nil {
			return fmt.Errorf("verified actionable plan contains bead %s more than once", beadID)
		}
		match = &actionable[i]
	}
	if match == nil {
		return fmt.Errorf("bead %s is absent from the verified actionable plan", beadID)
	}
	if len(match.BlockedBy) > 0 {
		return fmt.Errorf("bead %s is blocked in the verified plan by %s", beadID, strings.Join(match.BlockedBy, ", "))
	}
	for _, label := range match.Labels {
		if bv.IsOperatorGatedLabelInPolicy(label, operatorGatedLabels) {
			return fmt.Errorf("bead %s is operator-gated in the verified plan by label %q", beadID, strings.TrimSpace(label))
		}
	}
	status := strings.ToLower(strings.TrimSpace(match.Status))
	if status != "" && status != "open" && status != "ready" {
		return fmt.Errorf("bead %s has non-actionable plan status %q", beadID, match.Status)
	}
	return nil
}

func validateRestartBeadCommonWithPolicy(details *bv.BeadAssignmentDetails, now time.Time, operatorGatedLabels []string) error {
	if details == nil {
		return errors.New("live Beads assignment details are required")
	}
	beadID := strings.TrimSpace(details.ID)
	if len(details.BlockedBy) > 0 {
		return fmt.Errorf("bead %s has unresolved blockers: %s", beadID, strings.Join(details.BlockedBy, ", "))
	}
	for _, label := range details.Labels {
		if bv.IsOperatorGatedLabelInPolicy(label, operatorGatedLabels) {
			return fmt.Errorf("bead %s is operator-gated by live label %q", beadID, strings.TrimSpace(label))
		}
	}
	if details.DeferUntil != nil && details.DeferUntil.After(now) {
		return fmt.Errorf("bead %s is deferred until %s", beadID, details.DeferUntil.UTC().Format(time.RFC3339))
	}
	if details.Pinned {
		return fmt.Errorf("bead %s is pinned", beadID)
	}
	if details.Ephemeral {
		return fmt.Errorf("bead %s is ephemeral", beadID)
	}
	if details.Template {
		return fmt.Errorf("bead %s is a template", beadID)
	}
	if details.Wisp || strings.Contains(strings.ToLower(beadID), "-wisp-") {
		return fmt.Errorf("bead %s is a wisp", beadID)
	}
	return nil
}

func validateRestartFreshDetailsWithPolicy(details *bv.BeadAssignmentDetails, now time.Time, operatorGatedLabels []string) error {
	if err := validateRestartBeadCommonWithPolicy(details, now, operatorGatedLabels); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(details.Status), "open") {
		return fmt.Errorf("bead %s has status %q, want open", details.ID, details.Status)
	}
	if assignee := strings.TrimSpace(details.Assignee); assignee != "" {
		return fmt.Errorf("bead %s is already assigned to %q", details.ID, assignee)
	}
	return nil
}

func validateRestartRecoveryDetailsWithPolicy(details *bv.BeadAssignmentDetails, recovery *assignment.Assignment, now time.Time, operatorGatedLabels []string) error {
	if recovery == nil {
		return errors.New("durable restart recovery assignment is required")
	}
	if err := validateRestartBeadCommonWithPolicy(details, now, operatorGatedLabels); err != nil {
		return err
	}
	if recovery.ClaimState == assignment.ClaimIneligible || recovery.ClaimState == assignment.ClaimFailed {
		return fmt.Errorf("durable restart assignment %s has failed claim state %q", recovery.BeadID, recovery.ClaimState)
	}
	if strings.TrimSpace(recovery.ClaimActor) == "" ||
		!strings.EqualFold(strings.TrimSpace(details.Status), "in_progress") ||
		strings.TrimSpace(details.Assignee) != strings.TrimSpace(recovery.ClaimActor) {
		return fmt.Errorf("durable restart assignment %s is not owned in_progress by %s", recovery.BeadID, recovery.ClaimActor)
	}
	if recovery.DispatchState == assignment.DispatchSent &&
		(recovery.ClaimState != assignment.ClaimClaimed || strings.TrimSpace(recovery.DispatchReceiptID) == "" || strings.TrimSpace(recovery.PromptSent) == "") {
		return fmt.Errorf("durable restart assignment %s has an incomplete dispatch receipt", recovery.BeadID)
	}
	return nil
}

func restartAssignmentWasSent(recovery *assignment.Assignment) bool {
	return recovery != nil && recovery.DispatchState == assignment.DispatchSent
}

func applyRestartAssignmentReplay(output *RestartPaneOutput, recovery *assignment.Assignment) {
	if output == nil || recovery == nil {
		return
	}
	output.PromptSent = true
	output.ClaimActor = recovery.ClaimActor
	output.IdempotencyKey = recovery.IdempotencyKey
	output.DispatchReceiptID = recovery.DispatchReceiptID
	output.AssignmentReplayed = true
}

func applyRestartAtomicResult(output *RestartPaneOutput, result assignment.AtomicResult, executeErr error) {
	if output == nil {
		return
	}
	if result.Assignment != nil {
		output.ClaimActor = result.Assignment.ClaimActor
		output.IdempotencyKey = result.Assignment.IdempotencyKey
		output.DispatchReceiptID = result.Assignment.DispatchReceiptID
	}
	output.AssignmentReplayed = result.Replayed
	output.AssignmentRecovered = result.Recovered
	if executeErr != nil {
		output.PromptSent = false
		output.PromptError = executeErr.Error()
		if errors.Is(executeErr, context.Canceled) || errors.Is(executeErr, context.DeadlineExceeded) {
			setRestartPaneCancellation(output, executeErr, "restart canceled during atomic assignment")
			return
		}
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("atomic restart assignment failed: %w", executeErr),
			"ASSIGNMENT_FAILED",
			"Inspect the durable assignment receipt before retrying",
		)
		return
	}
	if !result.Sent || result.Assignment == nil || result.Assignment.DispatchState != assignment.DispatchSent ||
		strings.TrimSpace(result.Assignment.DispatchReceiptID) == "" {
		err := errors.New("atomic restart assignment completed without a durable dispatch receipt")
		output.PromptError = err.Error()
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Inspect the durable assignment ledger before retrying")
		return
	}
	output.PromptSent = true
}

// restartTargetIsAgent reports whether a resolved pane type identifies an
// agent CLI pane (as opposed to a user shell or an unidentifiable pane).
func restartTargetIsAgent(resolvedType string) bool {
	switch resolvedType {
	case "", "user", "unknown":
		return false
	default:
		return true
	}
}

func validateRestartBeadTargets(targets []tmux.Pane) error {
	if len(targets) != 1 {
		return fmt.Errorf("--restart-bead requires exactly one target pane, resolved %d", len(targets))
	}
	if !restartTargetIsAgent(restartPaneAgentType(targets[0])) {
		return fmt.Errorf("--restart-bead target %s is not an agent pane", targets[0].Ref().Physical())
	}
	return nil
}

func validateRestartPaneTargets(targets []tmux.Pane) error {
	for _, pane := range targets {
		resolvedType := restartPaneAgentType(pane)
		if pane.RotationToken != "" || pane.IsServicePane() {
			return fmt.Errorf("pane %s is owned by a rotation or service and cannot be restarted as an agent", pane.Ref().Physical())
		}
		if err := agent.AgentType(resolvedType).ValidateAutomatedRelaunch(); err != nil {
			return fmt.Errorf("pane %s (%s): %w", pane.Ref().Physical(), resolvedType, err)
		}
	}
	return nil
}

func respawnRestartPaneTargetsContext(
	ctx context.Context,
	targets []tmux.Pane,
	multiWindow bool,
	output *RestartPaneOutput,
	respawn func(context.Context, string, bool) error,
	panePID func(context.Context, string) (int, error),
) (map[string]restartPromptTarget, error) {
	if ctx == nil {
		return nil, errors.New("restart pane respawn context is required")
	}
	if respawn == nil {
		return nil, errors.New("restart pane respawn function is required")
	}
	if panePID == nil {
		return nil, errors.New("restart pane PID observer is required")
	}
	if err := validateRestartPaneTargets(targets); err != nil {
		return nil, err
	}

	restartedPaneInfo := make(map[string]restartPromptTarget)
	for paneIndex, pane := range targets {
		paneKey := paneTargetKey(pane, multiWindow)
		if err := ctx.Err(); err != nil {
			remaining := make([]string, 0, len(targets)-paneIndex)
			for _, pending := range targets[paneIndex:] {
				remaining = append(remaining, paneTargetKey(pending, multiWindow))
			}
			appendRestartCancellationFailures(output, remaining, 0, "respawn skipped", err)
			return restartedPaneInfo, err
		}
		beforePID, err := panePID(ctx, pane.ID)
		if err != nil || beforePID <= 0 {
			if err == nil {
				err = errors.New("pane PID is unavailable")
			}
			appendRestartFailureOnce(output, paneKey, fmt.Sprintf("failed to observe pane PID before respawn: %v", err))
			if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
				remaining := make([]string, 0, len(targets)-paneIndex-1)
				for _, pending := range targets[paneIndex+1:] {
					remaining = append(remaining, paneTargetKey(pending, multiWindow))
				}
				appendRestartCancellationFailures(output, remaining, 0, "respawn skipped", cancelErr)
				return restartedPaneInfo, cancelErr
			}
			continue
		}
		if pane.PID > 0 && beforePID != pane.PID {
			appendRestartFailureOnce(output, paneKey, "pane process changed after restart preflight; respawn was withheld")
			continue
		}
		if err := respawn(ctx, pane.ID, true); err != nil {
			if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
				afterPID, observationErr := observeRestartPanePIDAfterCancellation(ctx, pane.ID, panePID)
				if observationErr == nil && afterPID > 0 && afterPID != beforePID {
					output.Restarted = append(output.Restarted, paneKey)
					restartedPaneInfo[paneKey] = restartPromptTarget{
						Pane:         paneKey,
						Target:       pane.ID,
						AgentType:    tmux.AgentType(restartPaneAgentType(pane)).Canonical(),
						ResolvedType: restartPaneAgentType(pane),
						Variant:      pane.Variant,
						Width:        pane.Width,
						ExpectedPID:  afterPID,
					}
					appendRestartFailureOnce(output, paneKey, fmt.Sprintf(
						"respawn changed pane PID from %d to %d, but the command returned after cancellation and the post-respawn lifecycle is incomplete: %v",
						beforePID,
						afterPID,
						cancelErr,
					))
				} else {
					reason := fmt.Sprintf("failed to respawn: %v", err)
					switch {
					case observationErr != nil:
						reason += fmt.Sprintf("; post-cancellation pane PID observation failed, so mutation status is unknown: %v", observationErr)
					case afterPID <= 0:
						reason += "; post-cancellation pane PID is unavailable, so mutation status is unknown"
					default:
						reason += fmt.Sprintf("; pane PID remained %d", afterPID)
					}
					appendRestartFailureOnce(output, paneKey, reason)
				}
				remaining := make([]string, 0, len(targets)-paneIndex-1)
				for _, pending := range targets[paneIndex+1:] {
					remaining = append(remaining, paneTargetKey(pending, multiWindow))
				}
				appendRestartCancellationFailures(output, remaining, 0, "respawn skipped", cancelErr)
				return restartedPaneInfo, cancelErr
			}
			appendRestartFailureOnce(output, paneKey, fmt.Sprintf("failed to respawn: %v", err))
			continue
		}

		// Verify the respawn actually replaced the shell before counting the
		// pane as restarted: respawn-pane can report success while the pane
		// keeps its old process (a "soft restart"), which previously forced
		// operators to diff pane PIDs by hand (ntm-tgkb / AP-39). The check
		// runs only on a healthy context — cancellation falls through to the
		// existing mutated-but-incomplete bookkeeping below, and a failed
		// probe records Before-only evidence without blocking the restart.
		afterPID, afterErr := panePID(ctx, pane.ID)
		if output.PaneShellPIDs == nil {
			output.PaneShellPIDs = make(map[string]RestartPanePIDs)
		}
		pids := RestartPanePIDs{Before: beforePID}
		if afterErr == nil && afterPID > 0 {
			pids.After = afterPID
		}
		output.PaneShellPIDs[paneKey] = pids
		if ctx.Err() == nil && afterErr == nil && afterPID > 0 && afterPID == beforePID {
			appendRestartFailureOnce(output, paneKey, fmt.Sprintf(
				"respawn reported success but pane shell PID remained %d (soft restart); the pane was NOT replaced — escalate to a hard kill or inspect the pane", beforePID))
			continue
		}

		output.Restarted = append(output.Restarted, paneKey)
		restartedPaneInfo[paneKey] = restartPromptTarget{
			Pane:         paneKey,
			Target:       pane.ID,
			AgentType:    tmux.AgentType(restartPaneAgentType(pane)).Canonical(),
			ResolvedType: restartPaneAgentType(pane),
			Variant:      pane.Variant,
			Width:        pane.Width,
			ExpectedPID:  afterPID,
		}
		if err := ctx.Err(); err != nil {
			appendRestartFailureOnce(output, paneKey, fmt.Sprintf("respawn completed but post-respawn lifecycle was canceled: %v", err))
			remaining := make([]string, 0, len(targets)-paneIndex-1)
			for _, pending := range targets[paneIndex+1:] {
				remaining = append(remaining, paneTargetKey(pending, multiWindow))
			}
			appendRestartCancellationFailures(output, remaining, 0, "respawn skipped", err)
			return restartedPaneInfo, err
		}
	}
	return restartedPaneInfo, nil
}

func observeRestartPanePIDAfterCancellation(
	ctx context.Context,
	target string,
	panePID func(context.Context, string) (int, error),
) (int, error) {
	observationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartPaneMutationObservationTimeout)
	defer cancel()
	return panePID(observationCtx, target)
}

func relaunchRestartPaneAgentContext(
	ctx context.Context,
	info restartPromptTarget,
	launchCommand string,
	readyTimeout time.Duration,
	setAgentType func(context.Context, string, tmux.AgentType) error,
	send func(context.Context, string, string, bool, tmux.AgentType) error,
	panePID func(context.Context, string) (int, error),
	waitReady func(context.Context, string, int, string, time.Duration) (bool, error),
	observeProcess func(context.Context, string, int) (int, error),
) (restartAgentRelaunchOutcome, string, error) {
	failed := restartAgentRelaunchOutcome{Status: RestartAgentRelaunchFailed}
	if ctx == nil {
		return failed, "preflight", errors.New("agent relaunch context is required")
	}
	if setAgentType == nil || send == nil || panePID == nil || waitReady == nil || observeProcess == nil {
		return failed, "preflight", errors.New("agent relaunch dependencies are required")
	}

	shellPID, err := panePID(ctx, info.Target)
	if err != nil || shellPID <= 0 {
		if err == nil {
			err = errors.New("pane PID is unavailable")
		}
		return failed, "PID observation", err
	}
	if info.ExpectedPID > 0 && shellPID != info.ExpectedPID {
		return failed, "PID observation", errors.New("replacement pane process changed before agent launch")
	}
	if err := setAgentType(ctx, info.Target, tmux.AgentType(info.ResolvedType).Canonical()); err != nil {
		return failed, "identity", err
	}
	if err := send(ctx, info.Target, launchCommand, true, info.AgentType); err != nil {
		if restartPaneCancellationError(ctx, err) != nil {
			return observeRestartPaneAgentAfterCancellation(ctx, info, panePID, waitReady, observeProcess), "launch", err
		}
		return failed, "launch", err
	}

	ready, err := waitReady(ctx, info.Target, shellPID, info.ResolvedType, readyTimeout)
	agentPID, processErr := observeProcess(ctx, info.Target, shellPID)
	if err == nil {
		err = processErr
	}
	ready = ready && processErr == nil && agentPID > 0
	outcome := restartAgentRelaunchOutcome{
		Status:       RestartAgentRelaunchNotReady,
		Ready:        ready,
		ProcessAlive: processErr == nil && agentPID > 0,
		ShellPID:     shellPID,
	}
	if ready {
		outcome.Status = RestartAgentRelaunchReady
	}
	if err != nil && restartPaneCancellationError(ctx, err) != nil {
		return observeRestartPaneAgentAfterCancellation(ctx, info, panePID, waitReady, observeProcess), "readiness", err
	}
	return outcome, "readiness", err
}

func observeRestartPaneAgentAfterCancellation(
	ctx context.Context,
	info restartPromptTarget,
	panePID func(context.Context, string) (int, error),
	waitReady func(context.Context, string, int, string, time.Duration) (bool, error),
	observeProcess func(context.Context, string, int) (int, error),
) restartAgentRelaunchOutcome {
	outcome := restartAgentRelaunchOutcome{Status: RestartAgentRelaunchUnknown}
	observationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restartPaneMutationObservationTimeout)
	defer cancel()

	shellPID, err := panePID(observationCtx, info.Target)
	if err != nil || shellPID <= 0 {
		if err == nil {
			err = errors.New("pane PID is unavailable")
		}
		outcome.ObservationError = err
		return outcome
	}
	outcome.ShellPID = shellPID
	if info.ExpectedPID > 0 && shellPID != info.ExpectedPID {
		outcome.ObservationError = fmt.Errorf("replacement pane process changed: %w", tmux.ErrPaneIdentityChanged)
		return outcome
	}
	ready, readyErr := waitReady(observationCtx, info.Target, shellPID, info.ResolvedType, restartPaneMutationObservationTimeout)
	agentPID, processErr := observeProcess(observationCtx, info.Target, shellPID)
	outcome.ProcessAlive = processErr == nil && agentPID > 0
	outcome.Ready = ready && outcome.ProcessAlive

	switch {
	case outcome.Ready:
		outcome.Status = RestartAgentRelaunchReady
	case readyErr != nil || processErr != nil:
		outcome.Status = RestartAgentRelaunchUnknown
		for _, err := range []error{readyErr, processErr} {
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				outcome.ObservationError = err
				break
			}
		}
	case outcome.ProcessAlive:
		outcome.Status = RestartAgentRelaunchUnknown
	default:
		outcome.Status = RestartAgentRelaunchNotReady
	}
	return outcome
}

func formatRestartAgentLifecycleError(phase string, err error, outcome restartAgentRelaunchOutcome) string {
	if cancelErr := restartPaneCancellationError(context.Background(), err); cancelErr != nil {
		var reason string
		switch outcome.Status {
		case RestartAgentRelaunchReady:
			reason = fmt.Sprintf("agent became ready before %s returned cancellation, but the post-relaunch lifecycle is incomplete: %v", phase, cancelErr)
		case RestartAgentRelaunchUnknown:
			reason = fmt.Sprintf("agent %s returned cancellation; relaunch status is unknown and must be inspected before retrying: %v", phase, cancelErr)
		default:
			reason = fmt.Sprintf("agent relaunch was canceled during %s before readiness was confirmed: %v", phase, cancelErr)
		}
		if outcome.ObservationError != nil {
			reason += fmt.Sprintf("; independent observation failed: %v", outcome.ObservationError)
		}
		return reason
	}
	return fmt.Sprintf("failed during agent %s: %v", phase, err)
}

func appendRestartFailureOnce(output *RestartPaneOutput, pane, reason string) {
	if output == nil {
		return
	}
	for _, failure := range output.Failed {
		if failure.Pane == pane {
			return
		}
	}
	output.Failed = append(output.Failed, RestartError{Pane: pane, Reason: reason})
}

func appendRestartCancellationFailures(output *RestartPaneOutput, panes []string, start int, stage string, err error) {
	if output == nil || err == nil || start >= len(panes) {
		return
	}
	if start < 0 {
		start = 0
	}
	for _, pane := range panes[start:] {
		appendRestartFailureOnce(output, pane, fmt.Sprintf("%s: %v", stage, err))
	}
}

// restartModelVars recovers a restarted pane's model pin from its title
// variant. A restart must not silently downgrade a pinned pane to the account
// default model (#223), so when the variant is a known model alias (or an
// exact full model name from the alias table) the returned template vars carry
// the resolved pin. Unknown variants — persona names share the same title slot
// — fall back to the agent type's resolved default rather than guessing a bogus
// --model value.
func restartModelVars(cfg *config.Config, agentType, variant string) config.AgentTemplateVars {
	vars := config.AgentTemplateVars{AgentType: agentType}
	if cfg == nil {
		return vars
	}
	// Start from the agent type's resolved model. This is what keeps a
	// hard-pinned type usable: agy's template injects --model unconditionally, so
	// leaving Model empty relaunches it as `--model ''` and the pane dies at the
	// ready gate. For types whose templates guard with {{if .Model}}, an empty
	// result is still empty.
	vars.Model = cfg.Models.GetModelName(agentType, "")

	// The variant encodes `model@effort` when the pane was spawned with a
	// reasoning effort. Recovering the effort here is what stops a respawn from
	// silently relaunching the pane on the config DEFAULT budget (bd-qs6rj).
	alias, effort := tmux.ParsePaneVariant(variant)
	if effort != "" {
		// `model@effort` is written ONLY by the spawn path and only for an
		// explicit model spec, so the effort suffix is proof that the variant
		// names a model rather than a persona. A bare variant stays ambiguous
		// and keeps the historical persona-friendly handling below.
		vars.ReasoningEffort = effort
		vars.Model = cfg.Models.GetModelName(agentType, alias)
		if strings.TrimSpace(vars.Model) == "" {
			vars.Model = alias
		}
		vars.ModelAlias = alias
		vars.ModelRequested = true
		return vars
	}
	if alias == "" {
		return vars
	}
	aliases := cfg.Models.AliasesFor(agentType)
	if len(aliases) == 0 {
		return vars
	}
	if fullName, ok := aliases[strings.ToLower(alias)]; ok {
		vars.Model = fullName
		vars.ModelAlias = alias
		vars.ModelRequested = true
		return vars
	}
	for _, fullName := range aliases {
		if strings.EqualFold(fullName, alias) {
			vars.Model = fullName
			vars.ModelRequested = true
			return vars
		}
	}
	// A bare variant that matches neither an alias nor a configured full name
	// is a PERSONA name, not a model — spawn writes the persona there. Keeping
	// the configured default is deliberate: rendering `--model 'architect'`
	// would kill the pane at its ready gate. An explicit model spec is
	// distinguishable because spawn encodes it as `model@effort`, handled above.
	return vars
}

// restartAgentLaunchCommandWithOverride resolves the command used to relaunch
// an agent CLI in a respawned pane. It prefers the configured
// (template-rendered) agent command — the same command robot-spawn delivers by
// keystroke — rendered with the pane's recovered model pin (#223), and falls
// back to the canonical launch alias (cc/cod/gmi/...) when no usable command
// is configured (#187). It honors an explicit model/effort/args override
// (ntm-yusj). Without an override it preserves the historical
// silent-fallback-to-alias behavior and never returns an error; with one,
// every failure that would drop the override is a loud error.
func restartAgentLaunchCommandWithOverride(cfg *config.Config, agentType, variant string, override restartLaunchOverride) (string, error) {
	alias := restartLaunchAlias(agentType)
	resolved := ResolveAgentType(agentType)
	if err := validateRestartExtraArguments(override.Args, resolved); err != nil {
		return "", err
	}

	var tmpl string
	if cfg != nil {
		switch resolved {
		case "claude":
			tmpl = cfg.Agents.Claude
		case "codex":
			tmpl = cfg.Agents.Codex
		case "gemini":
			tmpl = cfg.Agents.Gemini
		case "antigravity":
			tmpl = cfg.Agents.Antigravity
		case "grok":
			tmpl = cfg.Agents.Grok
		case "cursor":
			tmpl = cfg.Agents.Cursor
		case "windsurf":
			tmpl = cfg.Agents.Windsurf
		case "aider":
			tmpl = cfg.Agents.Aider
		case "oc":
			// Fall back to the model-aware default when [agents] oc is unset
			// so a respawn launches the real `opencode` binary rather than the
			// bare `oc` alias. See ntm#193.
			tmpl = cfg.Agents.Opencode
			if strings.TrimSpace(tmpl) == "" {
				tmpl = config.DefaultOpencodeCommand
			}
		case "omp":
			tmpl = config.OmpCommandOrDefault(cfg.Agents.Omp)
		case "ollama":
			tmpl = cfg.Agents.Ollama
		}
	}
	appendArgs := func(cmd string) string {
		if override.Args != "" {
			return cmd + " " + override.Args
		}
		return cmd
	}

	if strings.TrimSpace(tmpl) == "" {
		if override.Model == "" && override.Effort == "" {
			return appendArgs(alias), nil
		}
		flags, err := restartOverrideAppendFlags(resolved, override, true, true)
		if err != nil {
			return "", err
		}
		return appendArgs(alias + flags), nil
	}

	vars := restartModelVars(cfg, resolved, variant)
	if resolved == "claude" {
		// A respawned Claude agent keeps ntm's PreToolUse hooks.
		vars.ClaudeSettings = policy.ClaudeAgentLaunchSettings(cfg).Settings
	}
	referencesModel := strings.Contains(tmpl, ".Model")
	referencesEffort := strings.Contains(tmpl, ".ReasoningEffort")
	if override.Model != "" {
		resolvedModel := override.Model
		if cfg != nil {
			if fullName := cfg.Models.GetModelName(resolved, override.Model); strings.TrimSpace(fullName) != "" {
				resolvedModel = fullName
			}
		}
		vars.Model = resolvedModel
		vars.ModelAlias = override.Model
		// ModelRequested triggers GenerateAgentCommand's silent-drop guard;
		// only assert it when the template can actually render the model.
		// Otherwise the override is carried by appended flags below.
		vars.ModelRequested = referencesModel
	}
	if override.Effort != "" {
		vars.ReasoningEffort = override.Effort
	}
	// Mirror the ModelRequested treatment above, for BOTH sources of an effort:
	// an explicit override AND one recovered from the pane title. Only hand the
	// effort to the renderer when the template can actually place it —
	// otherwise GenerateAgentCommand's silent-drop guard fires on an effort
	// this function fully intends to carry itself, as appended flags below.
	if !referencesEffort {
		vars.ReasoningEffort = ""
	}

	rendered, err := config.GenerateAgentCommand(tmpl, vars)
	if err == nil {
		policy.LogClaudeHooksNotApplied("restart", rendered, vars.ClaudeSettings)
	}
	if err != nil || strings.TrimSpace(rendered) == "" {
		if override.empty() {
			return alias, nil
		}
		if err == nil {
			err = fmt.Errorf("configured %s agent command rendered empty", resolved)
		}
		return "", fmt.Errorf("render relaunch command with override: %w", err)
	}

	if override.Model != "" || override.Effort != "" {
		if err := validateRestartOverrideCommand(rendered, resolved); err != nil {
			return "", err
		}
		rendered, err = removeRestartScalarOverrides(rendered, resolved, override.Model != "", override.Effort != "")
		if err != nil {
			return "", err
		}
		effectiveOverride := override
		if override.Model != "" {
			effectiveOverride.Model = vars.Model
		}
		flags, err := restartOverrideAppendFlags(resolved, effectiveOverride, override.Model != "", override.Effort != "")
		if err != nil {
			return "", err
		}
		rendered += flags
	}
	if override.Args != "" {
		if err := validateRestartOverrideCommand(rendered, resolved); err != nil {
			return "", err
		}
	}
	rendered = appendArgs(rendered)

	if _, err := tmux.SanitizePaneCommand(rendered); err != nil {
		if override.empty() {
			return alias, nil
		}
		return "", fmt.Errorf("relaunch command with override failed sanitization: %w", err)
	}
	return rendered, nil
}

// paneShellPIDContext queries the pane's current shell PID from tmux. After
// respawn-pane the shell PID changes, so callers must query it fresh rather
// than use the pre-restart pane snapshot.
func paneShellPIDContext(ctx context.Context, target string) (int, error) {
	if ctx == nil {
		return 0, errors.New("pane PID context is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	pidStr, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-t", tmux.ExactTarget(target), "-p", "#{pane_pid}")
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
	if err != nil || pid <= 0 {
		return 0, nil
	}
	return pid, nil
}

// waitForPaneAgentReadyContext requires fresh visible readiness text and a
// stable running foreground process belonging to the expected pane root.
// A stopped launch shell or an unrelated background child is not agent boot.
func waitForPaneAgentReadyContext(ctx context.Context, target string, shellPID int, agentType string, timeout time.Duration) (bool, error) {
	return waitForPaneAgentReadyWithContext(
		ctx,
		target,
		shellPID,
		agentType,
		timeout,
		restartPaneReadyPollInterval,
		tmux.CapturePaneVisibleContext,
		observeRestartForegroundProcess,
	)
}

// observeRestartForegroundProcess binds process evidence to tmux's current
// physical pane/root PID before looking under that root. Shell wrappers remain
// valid when a running non-shell descendant owns their terminal. The bounded
// walk does not require a particular executable name: custom launch commands
// and provider runtime wrappers are supported.
func observeRestartForegroundProcess(ctx context.Context, target string, expectedPID int) (int, error) {
	if ctx == nil {
		return 0, errors.New("restart process observation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if expectedPID <= 0 {
		return 0, errors.New("restart readiness requires a positive pane root PID")
	}
	ctx, cancel := context.WithTimeout(ctx, restartPaneMutationObservationTimeout)
	defer cancel()
	snapshot, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-t", tmux.ExactTarget(target), "-p",
		"#{pane_pid}\t#{pane_dead}\t#{pane_current_command}")
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("cannot observe restart pane process")
	}
	fields := strings.Split(strings.TrimRight(snapshot, "\r\n"), "\t")
	if len(fields) != 3 {
		return 0, errors.New("restart pane process observation is incomplete")
	}
	rootPID, err := strconv.Atoi(fields[0])
	if err != nil || rootPID != expectedPID {
		return 0, fmt.Errorf("replacement pane process changed: %w", tmux.ErrPaneIdentityChanged)
	}
	command := strings.TrimSpace(fields[2])
	if fields[1] != "0" || command == "" || tmux.PaneCommandIsStarting(command) {
		return 0, nil
	}
	if tmux.DefaultClient.Remote != "" {
		// Remote PIDs belong to another host. Tmux's live non-shell
		// foreground observation is the available evidence; local /proc
		// must never be consulted to strengthen that remote claim.
		if tmux.PaneCommandIsShell(command) {
			return 0, nil
		}
		return rootPID, nil
	}

	type candidate struct {
		pid   int
		depth int
	}
	queue := []candidate{{pid: rootPID}}
	seen := map[int]bool{rootPID: true}
	for checked := 0; len(queue) > 0 && checked < 32; checked++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		current := queue[0]
		queue = queue[1:]
		argv := process.GetCmdlineContext(ctx, current.pid)
		state, _, stateErr := process.GetProcessState(current.pid)
		if len(argv) > 0 && stateErr == nil && state != "" &&
			state != "T" && state != "t" && state != "Z" && state != "X" && state != "x" &&
			!tmux.PaneCommandIsShell(strings.TrimPrefix(filepath.Base(argv[0]), "-")) &&
			!tmux.PaneCommandIsStarting(argv[0]) &&
			restartProcessIsForeground(ctx, rootPID, current.pid) {
			return current.pid, nil
		}
		if current.depth >= 4 {
			continue
		}
		for _, child := range process.GetChildPIDsContext(ctx, current.pid, 8) {
			if child <= 0 || seen[child] {
				continue
			}
			seen[child] = true
			queue = append(queue, candidate{pid: child, depth: current.depth + 1})
		}
	}
	return 0, ctx.Err()
}

func restartProcessIsForeground(ctx context.Context, rootPID, pid int) bool {
	rootForeground, _, rootOK := process.TerminalForeground(rootPID)
	foregroundGroup, group, processOK := process.TerminalForeground(pid)
	if rootOK && processOK {
		return rootForeground > 0 && foregroundGroup == rootForeground && group == rootForeground
	}
	// Darwin's process library has a foreground check but no terminal-name
	// implementation. One bounded ps snapshot supplies both tty ownership
	// and the '+' foreground flag without reading command arguments.
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	probe := exec.CommandContext(probeCtx, "ps", "-o", "pid=,tty=,stat=", "-p", strconv.Itoa(rootPID)+","+strconv.Itoa(pid))
	probe.WaitDelay = 100 * time.Millisecond
	snapshot, err := probe.Output()
	return err == nil && restartPSForegroundMatches(string(snapshot), rootPID, pid)
}

func restartPSForegroundMatches(snapshot string, rootPID, pid int) bool {
	var rootTTY, candidateTTY, candidateState string
	for _, line := range strings.Split(snapshot, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		observedPID, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if observedPID == rootPID {
			rootTTY = fields[1]
		}
		if observedPID == pid {
			candidateTTY, candidateState = fields[1], fields[2]
		}
	}
	if rootTTY == "" || rootTTY == "?" || rootTTY == "??" || rootTTY == "-" || candidateTTY != rootTTY || candidateState == "" {
		return false
	}
	switch candidateState[0] {
	case 'T', 't', 'Z', 'X', 'x':
		return false
	}
	return strings.Contains(candidateState, "+")
}

func executeRestartBeadAfterSafeObservation(
	ctx context.Context,
	session string,
	request assignment.AtomicRequest,
	timeout time.Duration,
	pollInterval time.Duration,
	observe func(context.Context, string) (statuspkg.SessionObservation, error),
	execute func(context.Context, assignment.AtomicRequest) (assignment.AtomicResult, error),
) (assignment.AtomicResult, error) {
	if execute == nil {
		return assignment.AtomicResult{}, errors.New("restart assignment executor is required")
	}
	if err := waitForRestartPaneSafeDispatchContext(ctx, session, request.Target, timeout, pollInterval, observe); err != nil {
		return assignment.AtomicResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return assignment.AtomicResult{}, err
	}
	return execute(ctx, request)
}

func waitForRestartPaneSafeDispatchContext(
	ctx context.Context,
	session string,
	paneID string,
	timeout time.Duration,
	pollInterval time.Duration,
	observe func(context.Context, string) (statuspkg.SessionObservation, error),
) error {
	if ctx == nil {
		return errors.New("restart dispatch observation context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(session) == "" || strings.TrimSpace(paneID) == "" {
		return errors.New("restart dispatch observation requires a session and pane ID")
	}
	if observe == nil {
		return errors.New("restart dispatch observer is required")
	}
	if timeout <= 0 {
		return errors.New("restart dispatch observation timeout must be positive")
	}
	if pollInterval <= 0 {
		pollInterval = restartPaneReadyPollInterval
	}

	gateCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		observation, observeErr := observe(gateCtx, session)
		if err := ctx.Err(); err != nil {
			return err
		}
		if gateCtx.Err() != nil {
			return fmt.Errorf("pane %s did not become safe to dispatch within %s", paneID, timeout)
		}
		if observeErr == nil &&
			statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, time.Now()) &&
			observation.SafeToDispatch(paneID) {
			return nil
		}
		if err := waitForRestartPaneDelay(gateCtx, pollInterval); err != nil {
			if parentErr := ctx.Err(); parentErr != nil {
				return parentErr
			}
			return fmt.Errorf("pane %s did not become safe to dispatch within %s", paneID, timeout)
		}
	}
}

func waitForPaneAgentReadyWithContext(
	ctx context.Context,
	target string,
	shellPID int,
	agentType string,
	timeout time.Duration,
	pollInterval time.Duration,
	capture func(context.Context, string) (string, error),
	observeProcess func(context.Context, string, int) (int, error),
) (bool, error) {
	if ctx == nil {
		return false, errors.New("restart readiness context is required")
	}
	if capture == nil || observeProcess == nil {
		return false, errors.New("restart readiness dependencies are required")
	}
	if shellPID <= 0 {
		return false, errors.New("restart readiness requires a positive pane root PID")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if pollInterval <= 0 {
		pollInterval = restartPaneReadyPollInterval
	}
	deadline := time.Now().Add(timeout)
	var previousPID, stableObservations int
	for {
		beforePID, observeErr := observeProcess(ctx, target, shellPID)
		if observeErr != nil {
			return false, observeErr
		}
		captured, captureErr := capture(ctx, target)
		if cancelErr := restartPaneCancellationError(ctx, captureErr); cancelErr != nil {
			return false, cancelErr
		}
		afterPID, observeErr := observeProcess(ctx, target, shellPID)
		if observeErr != nil {
			return false, observeErr
		}
		outputReady := isAgentReady(captured, agentType)
		if tmux.AgentType(agentType).Canonical() == tmux.AgentCodex {
			// A Codex startup banner can be printed before a wrapper finishes
			// initialization. Require the current composer or live working
			// chrome while the same foreground process surrounds the capture.
			outputReady = tmux.ComposerVisibleForDelivery(captured, tmux.AgentCodex) || agent.CodexActivelyWorking(captured, 0)
		}
		ready := captureErr == nil && outputReady && beforePID > 0 && beforePID == afterPID
		if ready {
			if previousPID == afterPID {
				stableObservations++
			} else {
				previousPID, stableObservations = afterPID, 1
			}
			if stableObservations >= 2 {
				return true, nil
			}
		} else {
			previousPID, stableObservations = 0, 0
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		if remaining < pollInterval {
			pollInterval = remaining
		}
		if err := waitForRestartPaneDelay(ctx, pollInterval); err != nil {
			return false, err
		}
	}
}

func waitForRestartPaneDelay(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		return errors.New("restart delay context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func selectRestartPaneTargets(panes []tmux.Pane, paneFilterMap map[string]bool, filterType string, all bool) []tmux.Pane {
	hasPaneFilter := len(paneFilterMap) > 0
	targetType := translateAgentTypeForStatus(filterType)

	// Topology-aware --panes matching (#172): a bare index selects a whole window
	// on multi-window layouts instead of broadcasting or no-op'ing.
	multiWindow := paneSessionIsMultiWindow(panes)
	filterTokens := make([]string, 0, len(paneFilterMap))
	for k := range paneFilterMap {
		filterTokens = append(filterTokens, k)
	}

	var targetPanes []tmux.Pane
	for _, pane := range panes {
		if hasPaneFilter && !paneMatchesAnyToken(pane, filterTokens, multiWindow) {
			continue
		}

		currentType := translateAgentTypeForStatus(restartPaneAgentType(pane))
		if targetType != "" && targetType != currentType {
			continue
		}

		// By default only restart agent panes. Explicit pane filters and --all opt out.
		if !all && !hasPaneFilter && targetType == "" {
			agentType := restartPaneAgentType(pane)
			if pane.Index == 0 && agentType == "unknown" {
				continue
			}
			if agentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
	}

	return targetPanes
}

func restartPaneAgentType(pane tmux.Pane) string {
	if resolved := ResolveAgentType(string(pane.Type)); resolved != "" && resolved != "unknown" {
		return resolved
	}
	return detectAgentType(pane.Title)
}

// restartPromptDeliveryReport is the per-pane outcome of ordinary post-restart
// prompt delivery. PaneFailures carries typed per-pane failures (withheld
// prompts and unconfirmed submissions) that must reach output.Failed so
// success:true can never coexist with a swallowed prompt.
type restartPromptDeliveryReport struct {
	Errors        []string
	CanceledPanes []string
	Status        map[string]RestartPromptDeliveryStatus
	PaneFailures  []RestartError
	// Withheld is true when at least one prompt was deliberately NOT typed
	// because the pane never became ready for delivery (foreground shell or
	// composer never visible). No keystrokes reached those panes.
	Withheld bool
}

func sendRestartPromptsContext(
	ctx context.Context,
	targets []restartPromptTarget,
	prompt string,
	send func(context.Context, string, string, tmux.AgentType) error,
	gate func(context.Context, restartPromptTarget) (bool, string, error),
	verify func(context.Context, restartPromptTarget) error,
) (restartPromptDeliveryReport, error) {
	report := restartPromptDeliveryReport{Status: make(map[string]RestartPromptDeliveryStatus, len(targets))}
	if ctx == nil {
		return report, errors.New("restart prompt context is required")
	}
	if send == nil {
		return report, errors.New("restart prompt sender is required")
	}
	skipPending := func(pending []restartPromptTarget, cause error) {
		for _, target := range pending {
			report.Errors = append(report.Errors, fmt.Sprintf("pane %s: prompt skipped: %v", target.Pane, cause))
			report.CanceledPanes = append(report.CanceledPanes, target.Pane)
			report.Status[target.Pane] = RestartPromptSkipped
		}
	}
	for targetIndex, target := range targets {
		if err := ctx.Err(); err != nil {
			skipPending(targets[targetIndex:], err)
			return report, err
		}

		// Delivery-readiness gate (bd-rf0ka): never type a prompt into a pane
		// whose foreground is still a shell, or whose agent TUI has not shown
		// a composer that can accept typed input. A swallowed prompt cost 8
		// minutes in the field, and a prompt typed into bare zsh produced
		// parse errors — both are worse than a typed, retryable failure.
		if gate != nil {
			ready, reason, gateErr := gate(ctx, target)
			if cancelErr := restartPaneCancellationError(ctx, gateErr); cancelErr != nil {
				skipPending(targets[targetIndex:], cancelErr)
				return report, cancelErr
			}
			if gateErr != nil {
				ready = false
				if strings.TrimSpace(reason) == "" {
					reason = gateErr.Error()
				}
			}
			if !ready {
				if strings.TrimSpace(reason) == "" {
					reason = "pane did not become ready for typed input"
				}
				failure := fmt.Sprintf("%s: prompt withheld, no keystrokes sent: %s", ErrCodeRestartPromptNotDelivered, reason)
				report.Errors = append(report.Errors, fmt.Sprintf("pane %s: %s", target.Pane, failure))
				report.Status[target.Pane] = RestartPromptSkipped
				report.PaneFailures = append(report.PaneFailures, RestartError{Pane: target.Pane, Reason: failure})
				report.Withheld = true
				continue
			}
		}

		if err := send(ctx, target.Target, prompt, target.AgentType); err != nil {
			if cancelErr := restartPaneCancellationError(ctx, err); cancelErr != nil {
				report.Errors = append(report.Errors, fmt.Sprintf(
					"pane %s: prompt delivery outcome is unknown after cancellation; inspect the pane before retrying: %v",
					target.Pane,
					cancelErr,
				))
				report.Status[target.Pane] = RestartPromptUnknown
				report.CanceledPanes = append(report.CanceledPanes, target.Pane)
				skipPending(targets[targetIndex+1:], cancelErr)
				return report, cancelErr
			}
			report.Errors = append(report.Errors, fmt.Sprintf("pane %s: %v", target.Pane, err))
			report.Status[target.Pane] = RestartPromptFailed
			continue
		}

		// Post-send submission verification (bd-rf0ka): keys reached the pane,
		// but codex/claude TUIs can strand the prompt in the composer. An
		// unconfirmed submission is a per-pane failure, not success.
		if verify != nil {
			if verifyErr := verify(ctx, target); verifyErr != nil {
				if cancelErr := restartPaneCancellationError(ctx, verifyErr); cancelErr != nil {
					report.Errors = append(report.Errors, fmt.Sprintf(
						"pane %s: prompt delivery outcome is unknown after cancellation during submission verification; inspect the pane before retrying: %v",
						target.Pane,
						cancelErr,
					))
					report.Status[target.Pane] = RestartPromptUnknown
					report.CanceledPanes = append(report.CanceledPanes, target.Pane)
					skipPending(targets[targetIndex+1:], cancelErr)
					return report, cancelErr
				}
				failure := fmt.Sprintf("prompt submission unconfirmed: %v", verifyErr)
				report.Errors = append(report.Errors, fmt.Sprintf("pane %s: %s", target.Pane, failure))
				report.Status[target.Pane] = RestartPromptUnknown
				report.PaneFailures = append(report.PaneFailures, RestartError{Pane: target.Pane, Reason: failure})
				continue
			}
		}
		report.Status[target.Pane] = RestartPromptDelivered
		if err := ctx.Err(); err != nil {
			skipPending(targets[targetIndex+1:], err)
			return report, err
		}
	}
	return report, nil
}

// paneCurrentCommandContext queries the pane's current foreground command
// (tmux #{pane_current_command}). Used by the delivery-readiness gate to
// refuse typing a prompt while the pane foreground is still a shell.
func paneCurrentCommandContext(ctx context.Context, target string) (string, error) {
	if ctx == nil {
		return "", errors.New("pane command context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-t", tmux.ExactTarget(target), "-p", "#{pane_current_command}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(cmd), nil
}

// waitForRestartPromptDeliveryReadyContext polls, bounded by timeout, until an
// agent pane can actually accept a typed prompt: the foreground process must
// NOT be a shell AND ComposerReadyForDelivery must report the composer (or
// working chrome) visible. Returns ready=false with the last observed reason
// when the deadline passes — the caller must then withhold the prompt.
func waitForRestartPromptDeliveryReadyContext(
	ctx context.Context,
	target restartPromptTarget,
	timeout time.Duration,
	pollInterval time.Duration,
	currentCommand func(context.Context, string) (string, error),
	composerReady func(context.Context, string, tmux.AgentType, int) (bool, string),
) (bool, string, error) {
	if ctx == nil {
		return false, "", errors.New("prompt delivery readiness context is required")
	}
	if currentCommand == nil || composerReady == nil {
		return false, "", errors.New("prompt delivery readiness dependencies are required")
	}
	if pollInterval <= 0 {
		pollInterval = restartPaneReadyPollInterval
	}
	deadline := time.Now().Add(timeout)
	lastReason := "pane readiness could not be observed"
	for {
		if err := ctx.Err(); err != nil {
			return false, lastReason, err
		}
		cmd, cmdErr := currentCommand(ctx, target.Target)
		if cancelErr := restartPaneCancellationError(ctx, cmdErr); cancelErr != nil {
			return false, lastReason, cancelErr
		}
		switch {
		case cmdErr != nil:
			lastReason = fmt.Sprintf("pane foreground process could not be observed: %v", cmdErr)
		case strings.TrimSpace(cmd) == "":
			lastReason = "pane foreground process is unavailable (pane process may have exited)"
		case tmux.PaneCommandIsShell(cmd):
			lastReason = fmt.Sprintf("pane foreground process is a shell (%q), not the relaunched agent; typing the prompt would hit the shell", strings.TrimSpace(cmd))
		default:
			ready, reason := composerReady(ctx, target.Target, target.AgentType, target.Width)
			if err := ctx.Err(); err != nil {
				return false, lastReason, err
			}
			if ready {
				return true, "", nil
			}
			lastReason = strings.TrimSpace(reason)
			if lastReason == "" {
				lastReason = "agent composer is not ready for typed input"
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, fmt.Sprintf("pane did not become ready for prompt delivery within %s: %s", timeout, lastReason), nil
		}
		if remaining < pollInterval {
			pollInterval = remaining
		}
		if err := waitForRestartPaneDelay(ctx, pollInterval); err != nil {
			return false, lastReason, err
		}
	}
}

// restartPaneBeadPromptTemplate is the default prompt template for --bead assignment.
const restartPaneBeadPromptTemplate = "Read AGENTS.md, register with Agent Mail. Work on: {bead_id} - {bead_title}.\nUse br show {bead_id} for details. Mark in_progress when starting. Use ultrathink."

func buildRestartBeadPrompt(beadID, title string) string {
	return strings.NewReplacer(
		"{bead_id}", beadID,
		"{bead_title}", title,
	).Replace(restartPaneBeadPromptTemplate)
}

// PrintRestartPaneContext prints the cancellation-aware restart result.
func PrintRestartPaneContext(ctx context.Context, opts RestartPaneOptions) error {
	output, err := GetRestartPaneContext(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot restart-pane failed")
}
