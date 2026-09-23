// Package context provides context window monitoring for AI agent orchestration.
// rotation.go implements seamless agent rotation when context window is exhausted.
package context

import (
	stdcontext "context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// RotationMethod identifies how the rotation was triggered.
type RotationMethod string

const (
	// RotationThresholdExceeded indicates rotation due to context threshold.
	RotationThresholdExceeded RotationMethod = "threshold_exceeded"
	// RotationManual indicates a manually triggered rotation.
	RotationManual RotationMethod = "manual"
	// RotationCompactionFailed indicates rotation after compaction failed.
	RotationCompactionFailed RotationMethod = "compaction_failed"
)

// RotationState tracks the current state of a rotation.
type RotationState string

const (
	RotationStatePending    RotationState = "pending"
	RotationStateInProgress RotationState = "in_progress"
	RotationStateCompleted  RotationState = "completed"
	RotationStateFailed     RotationState = "failed"
	RotationStateAborted    RotationState = "aborted"
)

// RotationResult contains the outcome of a rotation attempt.
type RotationResult struct {
	Success       bool           `json:"success"`
	OldAgentID    string         `json:"old_agent_id"`
	NewAgentID    string         `json:"new_agent_id,omitempty"`
	OldPaneID     string         `json:"old_pane_id"`
	NewPaneID     string         `json:"new_pane_id,omitempty"`
	Method        RotationMethod `json:"method"`
	State         RotationState  `json:"state"`
	SummaryTokens int            `json:"summary_tokens,omitempty"`
	Duration      time.Duration  `json:"duration"`
	Error         string         `json:"error,omitempty"`
	Timestamp     time.Time      `json:"timestamp"`
}

// RotationEvent represents a rotation for audit/history purposes.
type RotationEvent struct {
	SessionName   string         `json:"session_name"`
	OldAgentID    string         `json:"old_agent_id"`
	NewAgentID    string         `json:"new_agent_id"`
	AgentType     string         `json:"agent_type"`
	Method        RotationMethod `json:"method"`
	ContextBefore float64        `json:"context_before"` // Usage percentage before
	ContextAfter  float64        `json:"context_after"`  // Usage percentage after (should be ~0)
	SummaryTokens int            `json:"summary_tokens"`
	Duration      time.Duration  `json:"duration"`
	Timestamp     time.Time      `json:"timestamp"`
	Error         string         `json:"error,omitempty"`
}

// ConfirmAction represents the action to take for a pending rotation.
type ConfirmAction string

const (
	// ConfirmRotate proceeds with the rotation.
	ConfirmRotate ConfirmAction = "rotate"
	// ConfirmCompact tries compaction instead of rotation.
	ConfirmCompact ConfirmAction = "compact"
	// ConfirmIgnore cancels the rotation and continues as-is.
	ConfirmIgnore ConfirmAction = "ignore"
	// ConfirmPostpone delays the rotation by a specified duration.
	ConfirmPostpone ConfirmAction = "postpone"
)

// PendingRotation represents a rotation awaiting user confirmation.
type PendingRotation struct {
	AgentID        string          `json:"agent_id"`
	SessionName    string          `json:"session_name"`
	PaneID         string          `json:"pane_id"`
	ContextPercent float64         `json:"context_percent"`
	CreatedAt      time.Time       `json:"created_at"`
	TimeoutAt      time.Time       `json:"timeout_at"`
	DefaultAction  ConfirmAction   `json:"default_action"`
	WorkDir        string          `json:"-"` // Not serialized
	PanePID        int             `json:"pane_pid,omitempty"`
	PaneType       string          `json:"pane_type,omitempty"`
	Remote         string          `json:"remote,omitempty"`
	SelectedAction ConfirmAction   `json:"selected_action,omitempty"`
	ExecutionState RotationState   `json:"execution_state,omitempty"`
	ExecutionID    string          `json:"execution_id,omitempty"`
	Result         *RotationResult `json:"result,omitempty"`
}

// PendingRotationOutput provides robot mode JSON output for pending rotations.
type PendingRotationOutput struct {
	Type             string   `json:"type"`
	AgentID          string   `json:"agent_id"`
	SessionName      string   `json:"session_name"`
	ContextPercent   float64  `json:"context_percent"`
	AwaitingConfirm  bool     `json:"awaiting_confirmation"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	DefaultAction    string   `json:"default_action"`
	AvailableActions []string `json:"available_actions"`
	GeneratedAt      string   `json:"generated_at"`
}

// RemainingSeconds returns the seconds remaining before timeout.
func (p *PendingRotation) RemainingSeconds() int {
	remaining := int(time.Until(p.TimeoutAt).Seconds())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// IsExpired returns true if the pending rotation has timed out.
func (p *PendingRotation) IsExpired() bool {
	return p.ExecutionState == "" && time.Now().After(p.TimeoutAt)
}

func clonePendingRotation(p *PendingRotation) *PendingRotation {
	if p == nil {
		return nil
	}
	cloned := *p
	if p.Result != nil {
		result := *p.Result
		cloned.Result = &result
	}
	return &cloned
}

// PaneSpawner abstracts pane creation for testing.
type PaneSpawner interface {
	// SpawnAgent creates a new agent pane and returns its ID.
	SpawnAgent(session, agentType string, index int, variant string, workDir string) (paneID string, err error)
	// KillPane terminates a pane.
	KillPane(paneID string) error
	// SendKeys sends text to a pane.
	SendKeys(paneID, text string, enter bool) error
	// SendBuffer pastes text into a pane using tmux's buffer mechanism.
	SendBuffer(paneID, text string, enter bool) error
	// GetPanes returns all panes in a session.
	GetPanes(session string) ([]tmux.Pane, error)
}

// rotationPaneLifecycle lets a spawner bind replacement operations to the
// original process, replay its launch settings, and observe prompt readiness.
// Custom spawners that implement only PaneSpawner retain responsibility for
// their own startup and delivery protocol.
type rotationPaneLifecycle interface {
	SpawnReplacementContext(stdcontext.Context, string, tmux.Pane, int, string) (tmux.Pane, error)
	DeliverHandoffContext(stdcontext.Context, string, tmux.Pane, string) error
	KillRotationPaneContext(stdcontext.Context, string, tmux.Pane) error
}

type rotationContextTransport interface {
	GetPanesContext(stdcontext.Context, string) ([]tmux.Pane, error)
	SendBufferContext(stdcontext.Context, string, string, bool) error
	SendKeysContext(stdcontext.Context, string, string, bool) error
}

type paneInputSender interface {
	SendKeys(paneID, text string, enter bool) error
	SendBuffer(paneID, text string, enter bool) error
}

type tmuxPaneInputSender struct{}

// DefaultPaneSpawner implements PaneSpawner using the tmux package.
type DefaultPaneSpawner struct {
	config *config.Config
}

// NewDefaultPaneSpawner creates a PaneSpawner using the tmux package.
func NewDefaultPaneSpawner(cfg *config.Config) *DefaultPaneSpawner {
	return &DefaultPaneSpawner{config: cfg}
}

// SpawnAgent creates a new agent pane.
func (s *DefaultPaneSpawner) SpawnAgent(session, agentType string, index int, variant string, workDir string) (string, error) {
	if err := validateAutomatedRotation(agent.AgentType(agentType)); err != nil {
		return "", fmt.Errorf("spawning replacement %s agent: %w", agent.AgentType(agentType).Canonical(), err)
	}

	// Resolve the original launch spec before creating a pane. Configured
	// commands are Go templates, and passing them directly to the shell leaves
	// the replacement at a shell error instead of starting an agent.
	spec, err := s.agentLaunchSpec(session, agentType, index, variant, workDir)
	if err != nil {
		return "", fmt.Errorf("preparing replacement agent: %w", err)
	}
	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), rotationReadyTimeout)
	defer cancel()
	pane, err := s.spawnAgentWithSpec(ctx, session, index, variant, workDir, spec)
	return pane.ID, err
}

// SpawnReplacementContext uses creation-time metadata whenever it exists.
// Corrupt or incomplete metadata must never fall back to title inference: that
// would silently turn an exact launch into a different model, persona, or
// account. Only panes created before launch metadata existed use that fallback.
func (s *DefaultPaneSpawner) SpawnReplacementContext(ctx stdcontext.Context, session string, predecessor tmux.Pane, index int, workDir string) (tmux.Pane, error) {
	if ctx == nil {
		return tmux.Pane{}, errors.New("replacement launch context is required")
	}
	launchCtx, cancel := stdcontext.WithTimeout(ctx, rotationReadyTimeout)
	defer cancel()
	if err := validateAutomatedRotation(predecessor.Type); err != nil {
		return tmux.Pane{}, err
	}
	if _, err := currentRotationPane(launchCtx, session, predecessor, tmux.GetPanesContext); err != nil {
		return tmux.Pane{}, fmt.Errorf("original agent changed before replacement: %w", err)
	}
	// Agents may run in separate worktrees inside one session. The caller's
	// session/project directory is not evidence of this pane's working tree.
	observedDir, err := tmux.DefaultClient.RunContext(launchCtx, "display-message", "-p", "-t", tmux.ExactTarget(predecessor.ID), "#{pane_current_path}")
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("reading original agent working directory: %w", err)
	}
	workDir = strings.TrimSpace(observedDir)
	if workDir == "" || !filepath.IsAbs(workDir) {
		return tmux.Pane{}, errors.New("original agent working directory is unavailable or not absolute")
	}
	if _, err := currentRotationPane(launchCtx, session, predecessor, tmux.GetPanesContext); err != nil {
		return tmux.Pane{}, fmt.Errorf("original agent changed while reading its working directory: %w", err)
	}
	spec, err := tmux.ReadPaneLaunchSpecContext(launchCtx, predecessor.ID)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("reading original launch settings: %w", err)
	}
	if spec == nil {
		reconstructed, err := s.agentLaunchSpec(session, string(predecessor.Type), index, predecessor.Variant, workDir)
		if err != nil {
			return tmux.Pane{}, fmt.Errorf("reconstructing legacy launch settings: %w", err)
		}
		spec = &reconstructed
	}
	if err := spec.ValidateReplay(predecessor.Type); err != nil {
		return tmux.Pane{}, fmt.Errorf("cannot replay original launch settings: %w", err)
	}
	return s.spawnAgentWithSpec(launchCtx, session, index, predecessor.Variant, workDir, *spec)
}

func (s *DefaultPaneSpawner) spawnAgentWithSpec(ctx stdcontext.Context, session string, index int, variant, workDir string, spec tmux.AgentLaunchSpec) (tmux.Pane, error) {
	isolationSession := session
	if spec.ClaudeIsolateCredentials {
		// The old and new panes overlap. Reusing their logical title/index for
		// credential isolation would make them share a mutable credential dir.
		isolationSession = "ntm-rotation-" + rand.Text()
	}
	agentCmd, err := resilience.PrepareAgentLaunchSpec(ctx, s.config, spec, workDir, isolationSession, index)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("preparing replacement launch environment: %w", err)
	}
	cmd, err := tmux.BuildPaneCommand(workDir, agentCmd)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("building command: %w", err)
	}

	// Create a new pane
	paneID, err := tmux.SplitWindowContext(ctx, session, workDir)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("creating pane: %w", err)
	}
	pane := tmux.Pane{ID: paneID, Type: spec.AgentType}
	// Preserve uncertain panes for inspection. In particular, a failed identity
	// observation is not permission to kill a process that may have replaced it.
	fail := func(stage string, err error) (tmux.Pane, error) {
		return pane, fmt.Errorf("%s for replacement %s (pane preserved): %w", stage, paneID, err)
	}

	// Persist the replacement provider separately from its mutable title.
	shortType := agentTypeShort(string(spec.AgentType))
	title := tmux.FormatPaneName(session, shortType, index, variant)
	if err := tmux.SetPaneAgentIdentityContext(ctx, paneID, title, spec.AgentType); err != nil {
		return fail("setting pane identity", err)
	}
	if err := tmux.SetPaneLaunchSpecContext(ctx, paneID, spec); err != nil {
		return fail("persisting launch settings", err)
	}
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return fail("observing new pane", err)
	}
	observed, err := findLiveAgentPane(panes, title, paneID)
	if err != nil {
		return fail("pinning new pane", err)
	}
	pane = observed
	if pane.PID <= 0 || pane.Dead || pane.IsServicePane() || pane.Type.Canonical() != spec.AgentType.Canonical() || !pane.IdleShell() {
		return fail("checking new pane", errors.New("expected a live, unoccupied agent-tagged shell with a known process identity"))
	}

	// Launch the agent
	if err := tmux.SendKeysContext(ctx, paneID, cmd, true); err != nil {
		return fail("launching agent", err)
	}

	// Apply tiled layout (best-effort)
	if err := tmux.ApplyTiledLayoutContext(ctx, session); err != nil {
		slog.Warn("failed to apply tiled layout after spawn", "session", session, "error", err)
	}

	return pane, nil
}

// agentLaunchSpec restores the model, reasoning effort, and registered
// persona encoded in the predecessor's variant using the same model registry
// and guarded template renderer as ordinary agent creation.
func (s *DefaultPaneSpawner) agentLaunchSpec(session, agentType string, index int, variant, workDir string) (tmux.AgentLaunchSpec, error) {
	models := config.DefaultModels()
	if s.config != nil {
		models = s.config.Models
	}
	modelAlias, effort := tmux.ParsePaneVariant(variant)
	vars := config.AgentTemplateVars{
		ModelAlias:      modelAlias,
		ModelRequested:  modelAlias != "",
		ReasoningEffort: effort,
		SessionName:     session,
		PaneIndex:       index,
		AgentType:       agentTypeShort(agentType),
		ProjectDir:      workDir,
	}
	// A bare variant can name a persona. A model@effort variant records an
	// explicit model selection and is never interpreted as a persona.
	if modelAlias != "" && effort == "" {
		registry, err := persona.LoadRegistry(workDir)
		if err != nil {
			return tmux.AgentLaunchSpec{}, fmt.Errorf("loading replacement persona: %w", err)
		}
		if p, ok := registry.Get(modelAlias); ok && p != nil {
			// Bare titles do not record whether the operator selected a model
			// alias or a persona. Both can legitimately be named "architect",
			// including in the default configuration. Refuse a known collision
			// instead of injecting a persona into a model-only agent or changing
			// a persona's model. The original pane remains available to recover.
			ambiguous := strings.EqualFold(models.GetModelName(agentType, ""), modelAlias)
			for alias, model := range models.AliasesFor(agentType) {
				if strings.EqualFold(alias, modelAlias) || strings.EqualFold(model, modelAlias) {
					ambiguous = true
					break
				}
			}
			if ambiguous {
				return tmux.AgentLaunchSpec{}, fmt.Errorf("ambiguous replacement variant %q matches both a configured model and a persona; cannot safely restore the original launch settings", modelAlias)
			}
			if agent.AgentType(p.AgentType).Canonical() != agent.AgentType(agentType).Canonical() {
				return tmux.AgentLaunchSpec{}, fmt.Errorf("persona %q belongs to %s, cannot rotate a %s pane into that persona", p.Name, p.AgentType, agentType)
			}
			vars.PersonaName = p.Name
			vars.ModelAlias = strings.TrimSpace(p.Model)
			vars.ModelRequested = vars.ModelAlias != ""
			vars.ReasoningEffort = strings.TrimSpace(p.ReasoningEffort)
			vars.SystemPrompt = p.SystemPrompt
			vars.SystemPromptFile, err = persona.PrepareSystemPrompt(p, workDir)
			if err != nil {
				return tmux.AgentLaunchSpec{}, fmt.Errorf("preparing replacement persona %q: %w", p.Name, err)
			}
		}
	}
	if agent.AgentType(agentType).Canonical() == agent.AgentTypeAntigravity &&
		vars.ModelAlias != "" && vars.ModelAlias != config.AntigravityRequiredModel {
		return tmux.AgentLaunchSpec{}, fmt.Errorf("antigravity model is pinned to %q, cannot restore model %q", config.AntigravityRequiredModel, vars.ModelAlias)
	}
	vars.Model = models.GetModelName(agentType, vars.ModelAlias)
	command, err := config.GenerateAgentCommand(s.getAgentCommand(agentType), vars)
	if err != nil {
		return tmux.AgentLaunchSpec{}, err
	}
	if strings.TrimSpace(command) == "" {
		return tmux.AgentLaunchSpec{}, fmt.Errorf("configured %s agent command rendered empty", agentType)
	}
	spec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentType(agentType).Canonical(),
		Command: command, Model: vars.Model, ModelAlias: vars.ModelAlias,
		Persona: vars.PersonaName, ReasoningEffort: vars.ReasoningEffort,
	}
	if vars.SystemPromptFile != "" {
		prompt, err := os.ReadFile(vars.SystemPromptFile)
		if err != nil {
			return tmux.AgentLaunchSpec{}, fmt.Errorf("recording replacement system prompt: %w", err)
		}
		if len(prompt) > 1<<20 {
			return tmux.AgentLaunchSpec{}, errors.New("replacement system prompt exceeds 1 MiB")
		}
		spec.SystemPromptFile = vars.SystemPromptFile
		spec.SystemPromptSHA256 = fmt.Sprintf("%x", sha256.Sum256(prompt))
	}
	if s.config != nil && spec.AgentType == tmux.AgentClaude && s.config.Agents.ClaudeIsolateCredentials {
		spec.ClaudeIsolateCredentials = true
		spec.ClaudeTokenFile, err = swarm.ResolveClaudeSetupTokenFile(s.config.Agents.ClaudeTokenFile)
		if err != nil {
			return tmux.AgentLaunchSpec{}, fmt.Errorf("recording replacement token-file reference: %w", err)
		}
	}
	return spec, nil
}

// KillPane terminates a pane.
func (s *DefaultPaneSpawner) KillPane(paneID string) error {
	return tmux.KillPane(paneID)
}

// SendKeys sends text to a pane.
func (s *DefaultPaneSpawner) SendKeys(paneID, text string, enter bool) error {
	return tmux.SendKeys(paneID, text, enter)
}

// SendBuffer pastes text into a pane using tmux's buffer mechanism.
func (s *DefaultPaneSpawner) SendBuffer(paneID, text string, enter bool) error {
	return tmux.SendBuffer(paneID, text, enter)
}

// GetPanes returns all panes in a session.
func (s *DefaultPaneSpawner) GetPanes(session string) ([]tmux.Pane, error) {
	return tmux.GetPanes(session)
}

func (s *DefaultPaneSpawner) GetPanesContext(ctx stdcontext.Context, session string) ([]tmux.Pane, error) {
	return tmux.GetPanesContext(ctx, session)
}

func (s *DefaultPaneSpawner) SendBufferContext(ctx stdcontext.Context, paneID, text string, enter bool) error {
	return tmux.DefaultClient.SendBufferContext(ctx, paneID, text, enter)
}

func (s *DefaultPaneSpawner) SendKeysContext(ctx stdcontext.Context, paneID, text string, enter bool) error {
	return tmux.SendKeysContext(ctx, paneID, text, enter)
}

const (
	rotationReadyTimeout = 45 * time.Second
	rotationReadyPoll    = 200 * time.Millisecond
)

type rotationPaneLister func(stdcontext.Context, string) ([]tmux.Pane, error)
type rotationPaneCapture func(stdcontext.Context, string) (string, error)
type rotationOutputCapture func(stdcontext.Context, string, int) (string, error)

func rotationProcessRunning(pane tmux.Pane) bool {
	command := strings.TrimSpace(pane.Command)
	if command == "" || tmux.PaneCommandIsStarting(command) {
		return false
	}
	// Match the shared startup observer's process evidence: a sh -c wrapper
	// can own the foreground process group without changing its command name.
	return !tmux.PaneCommandIsShell(command) || pane.ForegroundJobRunning()
}

// currentRotationPane never substitutes a title/index match for a missing
// physical pane and never accepts a respawn of that pane's original process.
func currentRotationPane(ctx stdcontext.Context, session string, expected tmux.Pane, list rotationPaneLister) (tmux.Pane, error) {
	if expected.ID == "" || expected.PID <= 0 {
		return tmux.Pane{}, errors.New("pane process identity is unavailable")
	}
	panes, err := list(ctx, session)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("observe pane %s: %w", expected.ID, err)
	}
	pane, err := findLiveAgentPane(panes, "", expected.ID)
	if err != nil {
		return tmux.Pane{}, err
	}
	if pane.PID != expected.PID || pane.Type.Canonical() != expected.Type.Canonical() || pane.IsServicePane() {
		return tmux.Pane{}, fmt.Errorf("pane %s process or agent identity changed", expected.ID)
	}
	if pane.Dead {
		return tmux.Pane{}, fmt.Errorf("pane %s process exited", expected.ID)
	}
	return pane, nil
}

// rotationPromptReady is stricter than ordinary best-effort prompt delivery.
// Retiring the predecessor requires successful, nonempty observations of a
// ready input prompt; blank output, boot banners and dialogs are not evidence.
func rotationPromptReady(captured string, pane tmux.Pane) (bool, string) {
	if strings.TrimSpace(captured) == "" {
		return false, "agent has not drawn its input prompt"
	}
	if gate, found := agent.DetectInteractiveGate(captured, pane.Width); found {
		return false, "agent is showing " + gate
	}
	working := false
	switch pane.Type.Canonical() {
	case agent.AgentTypeClaudeCode:
		working = agent.ClaudeActivelyWorking(captured, pane.Width)
	case agent.AgentTypeCodex:
		working = agent.CodexActivelyWorking(captured, pane.Width)
	case agent.AgentTypeGrok:
		working = agent.GrokActivelyWorking(captured, pane.Width)
	case agent.AgentTypeOmp:
		working = agent.OmpActivelyWorking(captured, pane.Width)
	case agent.AgentTypeAntigravity:
		working = agent.AntigravityActivelyWorking(captured, pane.Width)
	case agent.AgentTypeOpencode:
		working = agent.OpencodeActivelyWorking(captured, pane.Width)
	}
	if working {
		return false, "agent is still processing a turn"
	}
	parser := agent.NewParser()
	observed := parser.DetectAgentType(captured)
	if observed.IsValid() && observed != agent.AgentTypeUser && observed.Canonical() != pane.Type.Canonical() {
		return false, fmt.Sprintf("visible output belongs to %s, expected %s", observed, pane.Type.Canonical())
	}
	composer := tmux.InspectComposer(captured, pane.Type)
	switch pane.Type.Canonical() {
	case agent.AgentTypeClaudeCode, agent.AgentTypeCodex, agent.AgentTypeGrok, agent.AgentTypeOmp:
		if !composer.MarkerVisible {
			return false, "agent composer is not visible"
		}
	default:
		// For providers without a structural composer parser, corroborate
		// their prompt with an independently recognized banner or executable.
		// A node/python REPL's generic '>' is not an agent-ready signal.
		commandType := agent.AgentType(filepath.Base(strings.TrimSpace(pane.Command))).Canonical()
		if commandType != pane.Type.Canonical() && observed.Canonical() != pane.Type.Canonical() {
			return false, "input prompt has no recognizable agent identity"
		}
	}
	if composer.HoldsText || composer.QueuedMessages {
		return false, "agent has unsubmitted or queued input"
	}
	if pane.Type.Canonical() == agent.AgentTypeOmp {
		if box := agent.ParseOmpComposer(captured); box.Found && box.RowsBelow != 0 {
			return false, "agent composer has an open completion list"
		}
	}
	state, err := parser.ParseWithHint(captured, pane.Type)
	if err != nil || state == nil || !state.IsIdle || state.IsWorking || state.IsInError || state.IsRateLimited {
		return false, "agent has not reached an idle input prompt"
	}
	return true, ""
}

func waitForRotationReady(ctx stdcontext.Context, session string, expected tmux.Pane, timeout, poll time.Duration, list rotationPaneLister, capture rotationPaneCapture) (tmux.Pane, error) {
	if ctx == nil || list == nil || capture == nil {
		return tmux.Pane{}, errors.New("rotation readiness requires a context and observation dependencies")
	}
	if timeout <= 0 {
		timeout = rotationReadyTimeout
	}
	if poll <= 0 {
		poll = rotationReadyPoll
	}
	waitCtx, cancel := stdcontext.WithTimeout(ctx, timeout)
	defer cancel()
	stableCommand := ""
	stableObservations := 0
	lastReason := "agent startup has not been observed"
	for {
		if err := waitCtx.Err(); err != nil {
			return tmux.Pane{}, fmt.Errorf("replacement %s did not become ready: %s: %w", expected.ID, lastReason, err)
		}
		pane, err := currentRotationPane(waitCtx, session, expected, list)
		if err != nil {
			return tmux.Pane{}, err
		}
		ready := false
		command := strings.TrimSpace(pane.Command)
		if !rotationProcessRunning(pane) {
			lastReason = "pane is still at a shell or has no foreground command"
		} else {
			captured, err := capture(waitCtx, expected.ID)
			if err != nil {
				return tmux.Pane{}, fmt.Errorf("observe replacement input prompt: %w", err)
			}
			ready, lastReason = rotationPromptReady(captured, pane)
		}
		if ready {
			command = strings.ToLower(filepath.Base(command))
			if command != stableCommand {
				stableCommand, stableObservations = command, 0
			}
			stableObservations++
			if stableObservations >= 2 {
				// Capture is a subprocess too. Recheck the pinned identity and
				// foreground command after it, immediately before handoff.
				latest, err := currentRotationPane(waitCtx, session, expected, list)
				if err != nil {
					return tmux.Pane{}, err
				}
				if rotationProcessRunning(latest) && strings.ToLower(filepath.Base(strings.TrimSpace(latest.Command))) == command {
					return latest, nil
				}
				lastReason = "foreground command changed after observing the input prompt"
				stableCommand, stableObservations = "", 0
			}
		} else {
			stableCommand, stableObservations = "", 0
		}
		if err := waitForRotationDelay(waitCtx, poll); err != nil {
			return tmux.Pane{}, fmt.Errorf("replacement %s did not become ready: %s: %w", expected.ID, lastReason, err)
		}
	}
}

func rotationSummaryRequest(summary *SummaryGenerator) (prompt, startMarker, endMarker string) {
	requestID := rand.Text()[:16]
	startMarker, endMarker = "NTM_START_"+requestID, "NTM_END_"+requestID
	// Keep the marker tokens inline in the request. A terminal echo of these
	// instructions cannot masquerade as the standalone response boundary lines.
	prompt = summary.GeneratePrompt() + fmt.Sprintf("\n\nBegin your completed response with `%s` on its own line and end it with `%s` on its own line. Put only your actual handoff summary between those lines; do not repeat these instructions.", startMarker, endMarker)
	return prompt, startMarker, endMarker
}

func completedRotationSummary(captured, startMarker, endMarker string) string {
	lines := strings.Split(captured, "\n")
	start := -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case startMarker:
			start = i + 1
		case endMarker:
			if start >= 0 && start < i {
				return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
			}
		}
	}
	return ""
}

// waitForRotationSummary accepts only a fresh, complete response to this
// rotation's request. The old five-second capture could parse the echoed
// template's own questions as the answer and retire the only real context.
func waitForRotationSummary(ctx stdcontext.Context, session, agentID string, expected tmux.Pane, summary *SummaryGenerator, startMarker, endMarker string, poll time.Duration, list rotationPaneLister, capture rotationOutputCapture, visible rotationPaneCapture) (*HandoffSummary, error) {
	if summary == nil || ctx == nil || list == nil || capture == nil || visible == nil {
		return nil, errors.New("handoff summary requires a context and observation dependencies")
	}
	if poll <= 0 {
		poll = rotationReadyPoll
	}
	timeout := summary.promptTimeout
	if timeout <= 0 {
		timeout = rotationReadyTimeout
	}
	waitCtx, cancel := stdcontext.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if err := waitCtx.Err(); err != nil {
			return nil, fmt.Errorf("original agent did not return a complete handoff summary: %w", err)
		}
		pane, err := currentRotationPane(waitCtx, session, expected, list)
		if err != nil {
			return nil, err
		}
		if !rotationProcessRunning(pane) {
			return nil, errors.New("original agent exited while generating its handoff summary")
		}
		captured, err := capture(waitCtx, expected.ID, 500)
		if err != nil {
			return nil, fmt.Errorf("capture original agent handoff response: %w", err)
		}
		body := completedRotationSummary(captured, startMarker, endMarker)
		if body != "" {
			parsed := summary.ParseAgentResponse(agentID, agentTypeLong(string(expected.Type)), session, body)
			// Independently reject the template even if a narrow terminal wrapped
			// request marker tokens onto their own lines. Actual task/progress
			// sections are required; a fallback cannot authorize retirement.
			valid := strings.TrimSpace(parsed.CurrentTask) != "" && strings.TrimSpace(parsed.Progress) != "" &&
				!strings.Contains(body, "What task are you currently working on?") &&
				!strings.Contains(body, "What have you accomplished so far?") &&
				!strings.Contains(body, "HANDOFF SUMMARY REQUIRED")
			if valid {
				if _, err := waitForRotationReady(waitCtx, session, expected, timeout, poll, list, visible); err != nil {
					return nil, fmt.Errorf("original agent has not finished its handoff response: %w", err)
				}
				return parsed, nil
			}
		}
		if err := waitForRotationDelay(waitCtx, poll); err != nil {
			return nil, fmt.Errorf("original agent did not return a complete handoff summary: %w", err)
		}
	}
}

func waitForRotationDelay(ctx stdcontext.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// DeliverHandoffContext observes readiness before typing and uses the provider's
// existing submission check to catch input stranded in the composer. These are
// process/input observations, not an application-level acknowledgment of context.
func (s *DefaultPaneSpawner) DeliverHandoffContext(ctx stdcontext.Context, session string, replacement tmux.Pane, prompt string) error {
	ready, err := waitForRotationReady(ctx, session, replacement, rotationReadyTimeout, rotationReadyPoll, tmux.GetPanesContext, tmux.CapturePaneVisibleContext)
	if err != nil {
		return err
	}
	deliveryCtx, cancel := stdcontext.WithTimeout(ctx, rotationReadyTimeout)
	defer cancel()
	if err := tmux.DefaultClient.SendBufferContext(deliveryCtx, ready.ID, prompt, true); err != nil {
		return err
	}
	confirmed := true
	switch ready.Type.Canonical() {
	case agent.AgentTypeClaudeCode:
		confirmed, _, err = tmux.VerifyClaudeSubmissionContext(deliveryCtx, ready.ID, prompt, ready.Width)
	case agent.AgentTypeCodex:
		confirmed, _, err = tmux.VerifyCodexSubmissionContext(deliveryCtx, ready.ID, prompt, ready.Width)
	case agent.AgentTypeGrok:
		confirmed, _, err = tmux.VerifyGrokSubmissionContext(deliveryCtx, ready.ID, prompt, ready.Width)
	case agent.AgentTypeOmp:
		confirmed, _, err = tmux.VerifyOmpSubmissionContext(deliveryCtx, ready.ID, prompt, ready.Width)
	}
	if err != nil {
		return fmt.Errorf("checking handoff submission: %w", err)
	}
	if !confirmed {
		return errors.New("handoff remains unsubmitted in the replacement composer")
	}
	latest, err := currentRotationPane(deliveryCtx, session, replacement, tmux.GetPanesContext)
	if err != nil {
		return err
	}
	if !rotationProcessRunning(latest) {
		return errors.New("replacement agent exited during handoff")
	}
	captured, err := tmux.CapturePaneVisibleContext(deliveryCtx, latest.ID)
	if err != nil || strings.TrimSpace(captured) == "" {
		return errors.New("replacement input state could not be observed after handoff")
	}
	composer := tmux.InspectComposer(captured, latest.Type)
	if composer.HoldsText || composer.QueuedMessages {
		return errors.New("replacement still has unsubmitted or queued input after handoff")
	}
	if gate, found := agent.DetectInteractiveGate(captured, latest.Width); found {
		return fmt.Errorf("replacement is showing %s after handoff", gate)
	}
	state, err := agent.NewParser().ParseWithHint(captured, latest.Type)
	if err != nil || state == nil || state.IsInError || state.IsRateLimited || !state.IsIdle && !state.IsWorking {
		return errors.New("replacement did not retain a usable agent input state after handoff")
	}
	// A capture can race a respawn just as the earlier readiness capture can.
	// Revalidate after the final observation before permitting retirement.
	finalPane, err := currentRotationPane(deliveryCtx, session, replacement, tmux.GetPanesContext)
	if err != nil {
		return err
	}
	if !rotationProcessRunning(finalPane) {
		return errors.New("replacement agent exited after handoff observation")
	}
	return nil
}

func (s *DefaultPaneSpawner) KillRotationPaneContext(ctx stdcontext.Context, session string, pane tmux.Pane) error {
	if _, err := currentRotationPane(ctx, session, pane, tmux.GetPanesContext); err != nil {
		return fmt.Errorf("preserving pane whose identity cannot be verified: %w", err)
	}
	return tmux.KillPaneContext(ctx, pane.ID)
}

func (s *DefaultPaneSpawner) getAgentCommand(agentType string) string {
	canonical := agent.AgentType(agentType).Canonical()

	if s.config != nil {
		switch canonical {
		case agent.AgentTypeClaudeCode:
			if s.config.Agents.Claude != "" {
				return s.config.Agents.Claude
			}
		case agent.AgentTypeCodex:
			if s.config.Agents.Codex != "" {
				return s.config.Agents.Codex
			}
		case agent.AgentTypeGemini:
			if s.config.Agents.Gemini != "" {
				return s.config.Agents.Gemini
			}
		case agent.AgentTypeAntigravity:
			if s.config.Agents.Antigravity != "" {
				return s.config.Agents.Antigravity
			}
		case agent.AgentTypeGrok:
			if s.config.Agents.Grok != "" {
				return s.config.Agents.Grok
			}
		case agent.AgentTypeCursor:
			if s.config.Agents.Cursor != "" {
				return s.config.Agents.Cursor
			}
		case agent.AgentTypeWindsurf:
			if s.config.Agents.Windsurf != "" {
				return s.config.Agents.Windsurf
			}
		case agent.AgentTypeAider:
			if s.config.Agents.Aider != "" {
				return s.config.Agents.Aider
			}
		case agent.AgentTypeOllama:
			if s.config.Agents.Ollama != "" {
				return s.config.Agents.Ollama
			}
		case agent.AgentTypeOmp:
			return config.OmpCommandOrDefault(s.config.Agents.Omp)
		case agent.AgentTypeOpencode:
			if s.config.Agents.Opencode != "" {
				return s.config.Agents.Opencode
			}
		}
	}

	defaults := config.DefaultAgentTemplates()
	switch canonical {
	case agent.AgentTypeClaudeCode:
		return defaults.Claude
	case agent.AgentTypeCodex:
		return defaults.Codex
	case agent.AgentTypeGemini:
		return defaults.Gemini
	case agent.AgentTypeAntigravity:
		return defaults.Antigravity
	case agent.AgentTypeGrok:
		return defaults.Grok
	case agent.AgentTypeOmp:
		return defaults.Omp
	case agent.AgentTypeOpencode:
		return defaults.Opencode
	case agent.AgentTypeCursor:
		return defaults.Cursor
	case agent.AgentTypeWindsurf:
		return defaults.Windsurf
	case agent.AgentTypeAider:
		return defaults.Aider
	case agent.AgentTypeOllama:
		return defaults.Ollama
	}
	// Fall back to using the agent type name as the command.
	// This handles unknown/future agent types that match their CLI name.
	return strings.TrimSpace(agentType)
}

func sendCompactionCommandToPane(sender paneInputSender, paneID string, cmd CompactionCommand) error {
	if cmd.IsPrompt {
		return sender.SendBuffer(paneID, cmd.Command, true)
	}
	return sender.SendKeys(paneID, cmd.Command, true)
}

func sendRotationPrompt(spawner PaneSpawner, paneID, prompt string) error {
	return spawner.SendBuffer(paneID, prompt, true)
}

func sendRotationPromptContext(ctx stdcontext.Context, spawner PaneSpawner, paneID, prompt string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if transport, ok := spawner.(rotationContextTransport); ok {
		return transport.SendBufferContext(ctx, paneID, prompt, true)
	}
	return sendRotationPrompt(spawner, paneID, prompt)
}

func validateAutomatedRotation(agentType agent.AgentType) error {
	if err := agentType.ValidateAutomatedRelaunch(); err != nil {
		return err
	}
	return agentType.ValidateAutomatedPromptDelivery()
}

func findLiveAgentPane(panes []tmux.Pane, agentID, paneID string) (tmux.Pane, error) {
	if paneID != "" {
		for _, pane := range panes {
			if pane.ID == paneID {
				return pane, nil
			}
		}
		return tmux.Pane{}, fmt.Errorf("pane %s not found for agent %s", paneID, agentID)
	}
	for _, pane := range panes {
		if pane.Title == agentID {
			return pane, nil
		}
	}
	return tmux.Pane{}, fmt.Errorf("pane not found for agent %s", agentID)
}

func validateAutomatedRotationBatch(panes []tmux.Pane, agentInfos []AgentContextInfo) error {
	for _, info := range agentInfos {
		pane, err := findLiveAgentPane(panes, info.AgentID, info.PaneID)
		if err != nil {
			return err
		}
		if err := validateAutomatedRotation(pane.Type); err != nil {
			return fmt.Errorf("agent %s (%s): %w", info.AgentID, pane.Type.Canonical(), err)
		}
	}
	return nil
}

func (r *Rotator) resolveLiveAgentType(session, agentID, paneID string) (agent.AgentType, error) {
	return r.resolveLiveAgentTypeContext(stdcontext.Background(), session, agentID, paneID)
}

func (r *Rotator) resolveLiveAgentTypeContext(ctx stdcontext.Context, session, agentID, paneID string) (agent.AgentType, error) {
	if r.spawner == nil {
		return agent.AgentTypeUnknown, errors.New("no spawner available")
	}
	var panes []tmux.Pane
	var err error
	if transport, ok := r.spawner.(rotationContextTransport); ok {
		panes, err = transport.GetPanesContext(ctx, session)
	} else {
		panes, err = r.spawner.GetPanes(session)
	}
	if err != nil {
		return agent.AgentTypeUnknown, fmt.Errorf("get panes for session %s: %w", session, err)
	}
	pane, err := findLiveAgentPane(panes, agentID, paneID)
	if err != nil {
		return agent.AgentTypeUnknown, err
	}
	return pane.Type.Canonical(), nil
}

// agentTypeShort returns the short form for pane naming.
func agentTypeShort(agentType string) string {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "cc"
	case agent.AgentTypeCodex:
		return "cod"
	case agent.AgentTypeGemini:
		return "gmi"
	case agent.AgentTypeAntigravity:
		return "agy"
	case agent.AgentTypeOmp:
		return "omp"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOllama:
		return "ollama"
	case agent.AgentTypeUser:
		return "user"
	default:
		return strings.TrimSpace(agentType)
	}
}

// agentTypeLong returns the long form from short form.
func agentTypeLong(shortType string) string {
	switch agent.AgentType(shortType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return "claude"
	case agent.AgentTypeCodex:
		return "codex"
	case agent.AgentTypeGemini:
		return "gemini"
	case agent.AgentTypeAntigravity:
		return "antigravity"
	case agent.AgentTypeOmp:
		return "omp"
	case agent.AgentTypeCursor:
		return "cursor"
	case agent.AgentTypeWindsurf:
		return "windsurf"
	case agent.AgentTypeAider:
		return "aider"
	case agent.AgentTypeOllama:
		return "ollama"
	case agent.AgentTypeUser:
		return "user"
	default:
		return strings.TrimSpace(shortType)
	}
}

// Rotator coordinates agent rotation when context window is exhausted.
type Rotator struct {
	mu sync.RWMutex // Protects history and pending

	monitor   *ContextMonitor
	compactor *Compactor
	summary   *SummaryGenerator
	spawner   PaneSpawner
	config    config.ContextRotationConfig

	// History of rotations for audit
	history []RotationEvent

	// Pending rotations awaiting confirmation (keyed by agentID)
	pending    map[string]*PendingRotation
	confirming map[string]bool
	// The public confirmation service owns the durable claim and its outcome.
	durableConfirmation bool
	expectedSource      *tmux.Pane
}

// RotatorConfig holds configuration for creating a Rotator.
type RotatorConfig struct {
	Monitor   *ContextMonitor
	Compactor *Compactor
	Summary   *SummaryGenerator
	Spawner   PaneSpawner
	Config    config.ContextRotationConfig
}

// NewRotator creates a new Rotator with the given configuration.
func NewRotator(cfg RotatorConfig) *Rotator {
	if cfg.Summary == nil {
		cfg.Summary = NewSummaryGenerator(SummaryGeneratorConfig{
			MaxTokens: cfg.Config.SummaryMaxTokens,
		})
	}
	if cfg.Compactor == nil && cfg.Monitor != nil {
		cfg.Compactor = NewCompactor(cfg.Monitor, DefaultCompactorConfig())
	}

	return &Rotator{
		monitor:    cfg.Monitor,
		compactor:  cfg.Compactor,
		summary:    cfg.Summary,
		spawner:    cfg.Spawner,
		config:     cfg.Config,
		history:    make([]RotationEvent, 0),
		pending:    make(map[string]*PendingRotation),
		confirming: make(map[string]bool),
	}
}

// CheckAndRotate checks all agents and rotates those above the threshold.
// Returns the results of all rotation attempts.
// If RequireConfirm is enabled, agents needing rotation are added to pending
// and results have State=RotationStatePending until confirmed.
func (r *Rotator) CheckAndRotate(sessionName, workDir string) ([]RotationResult, error) {
	if r.monitor == nil {
		return nil, fmt.Errorf("no monitor available")
	}
	if r.spawner == nil {
		return nil, fmt.Errorf("no spawner available")
	}
	if !r.config.Enabled {
		return nil, nil // Rotation disabled
	}

	// First, process any expired pending rotations
	r.processExpiredPending(sessionName, workDir)

	// Surface agents approaching exhaustion even when they have not reached the
	// rotation threshold yet. The warning is keyed by agent and session, so
	// repeated checks refresh the same alert instead of creating duplicates.
	for _, agentInfo := range r.agentsEligibleForRotation(r.config.WarningThreshold * 100) {
		usagePercent := 0.0
		if agentInfo.Estimate != nil {
			usagePercent = agentInfo.Estimate.UsagePercent
		}
		alerts.EmitContextWarning(alerts.RotationAlertData{
			AgentID:      agentInfo.AgentID,
			Session:      sessionName,
			Pane:         agentInfo.PaneID,
			ContextUsage: usagePercent,
		})
	}

	// Find agents above rotate threshold
	// Note: r.config.RotateThreshold is 0.0-1.0, but AgentsAboveThreshold expects 0-100 percentage
	agentsToRotate := r.agentsEligibleForRotation(r.config.RotateThreshold * 100)
	if len(agentsToRotate) == 0 {
		return nil, nil // No agents need rotation
	}
	panes, err := r.spawner.GetPanes(sessionName)
	if err != nil {
		return nil, fmt.Errorf("rotation preflight failed: get panes: %w", err)
	}
	if err := validateAutomatedRotationBatch(panes, agentsToRotate); err != nil {
		return nil, fmt.Errorf("rotation preflight failed: %w", err)
	}

	var results []RotationResult

	// Process agents one at a time
	for _, agentInfo := range agentsToRotate {
		// Skip if already pending
		if r.HasPendingRotation(agentInfo.AgentID) {
			continue
		}

		// If confirmation is required, create a pending rotation instead
		if r.config.RequireConfirm {
			usagePercent := 0.0
			if agentInfo.Estimate != nil {
				usagePercent = agentInfo.Estimate.UsagePercent
			}
			pending := r.createPendingRotation(sessionName, agentInfo.AgentID, agentInfo.PaneID, usagePercent, workDir)
			results = append(results, RotationResult{
				OldAgentID: agentInfo.AgentID,
				Method:     RotationThresholdExceeded,
				State:      RotationStatePending,
				Timestamp:  pending.CreatedAt,
				Error:      fmt.Sprintf("awaiting confirmation, timeout in %ds", pending.RemainingSeconds()),
			})
			continue
		}

		// No confirmation required, rotate directly
		result := r.rotateAgent(sessionName, agentInfo.AgentID, workDir)
		results = append(results, result)
	}

	return results, nil
}

// agentsEligibleForRotation returns threshold-matching agents that have been
// monitored long enough to satisfy the configured minimum session age. A
// missing start time is treated conservatively as ineligible: rotating an
// agent whose age is unknown defeats the guard's purpose.
func (r *Rotator) agentsEligibleForRotation(threshold float64) []AgentContextInfo {
	agents := r.monitor.AgentsAboveThreshold(threshold)
	minAge := time.Duration(r.config.MinSessionAgeSec) * time.Second
	if minAge <= 0 {
		return agents
	}

	now := time.Now()
	eligible := make([]AgentContextInfo, 0, len(agents))
	for _, agent := range agents {
		if agent.SessionStart.IsZero() || now.Sub(agent.SessionStart) < minAge {
			continue
		}
		eligible = append(eligible, agent)
	}
	return eligible
}

// createPendingRotation creates a pending rotation entry for an agent.
func (r *Rotator) createPendingRotation(session, agentID, paneID string, contextPercent float64, workDir string) *PendingRotation {
	now := time.Now()
	timeoutSec := r.config.ConfirmTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 60 // Default to 60 seconds if not configured
	}

	defaultAction := ConfirmAction(r.config.DefaultConfirmAction)
	if defaultAction == "" {
		defaultAction = ConfirmRotate
	}

	pending := &PendingRotation{
		AgentID:        agentID,
		SessionName:    session,
		PaneID:         paneID,
		ContextPercent: contextPercent,
		CreatedAt:      now,
		TimeoutAt:      now.Add(time.Duration(timeoutSec) * time.Second),
		DefaultAction:  defaultAction,
		WorkDir:        workDir,
	}
	if _, ok := r.spawner.(*DefaultPaneSpawner); ok {
		pending.Remote = tmux.DefaultClient.Remote
	}
	if r.spawner != nil {
		if panes, err := r.spawner.GetPanes(session); err == nil {
			if pane, err := findLiveAgentPane(panes, agentID, paneID); err == nil && pane.PID > 0 && !pane.Dead && !pane.IsServicePane() {
				pending.PanePID = pane.PID
				pending.PaneType = string(pane.Type.Canonical())
			}
		}
	}

	r.mu.Lock()
	r.pending[agentID] = pending
	r.mu.Unlock()

	// Also persist to the pending rotation store for CLI access
	if err := AddPendingRotation(pending); err != nil {
		slog.Warn("failed to persist pending rotation", "agent", agentID, "error", err)
	}

	return pending
}

// processExpiredPending handles pending rotations that have timed out.
func (r *Rotator) processExpiredPending(_, _ string) {
	type expiredPendingAction struct {
		source    *PendingRotation
		snapshot  *PendingRotation
		action    ConfirmAction
		agentType agent.AgentType
	}

	now := time.Now()
	postponed := make([]*PendingRotation, 0)
	actions := make([]expiredPendingAction, 0)

	r.mu.RLock()
	for _, pending := range r.pending {
		if !pending.IsExpired() {
			continue
		}
		actions = append(actions, expiredPendingAction{
			source:   pending,
			snapshot: clonePendingRotation(pending),
			action:   pending.DefaultAction,
		})
	}
	r.mu.RUnlock()
	if spawner, ok := r.spawner.(*DefaultPaneSpawner); ok && !r.durableConfirmation {
		for _, action := range actions {
			result := confirmStoredRotation(stdcontext.Background(), action.snapshot.AgentID, action.action, 30, false, spawner.config, r, true)
			if !result.Success {
				slog.Warn("expired pending confirmation failed", "agent", action.snapshot.AgentID, "action", action.action, "error", result.Error)
			}
		}
		return
	}

	// Validate every expired lifecycle action before changing pending state.
	// A supported action must not run merely because map iteration encountered it
	// before a later unsupported Grok action in the same batch.
	for i := range actions {
		pendingAction := &actions[i]
		var err error
		switch pendingAction.action {
		case ConfirmRotate:
			pendingAction.agentType, err = r.resolveLiveAgentType(
				pendingAction.snapshot.SessionName,
				pendingAction.snapshot.AgentID,
				pendingAction.snapshot.PaneID,
			)
			if err == nil {
				err = validateAutomatedRotation(pendingAction.agentType)
			}
		case ConfirmCompact:
			pendingAction.agentType, err = r.resolveLiveAgentType(
				pendingAction.snapshot.SessionName,
				pendingAction.snapshot.AgentID,
				pendingAction.snapshot.PaneID,
			)
			if err == nil {
				err = pendingAction.agentType.ValidateAutomatedPromptDelivery()
			}
			if err == nil && !GetAgentCapabilities(string(pendingAction.agentType)).SupportsBuiltinCompact {
				err = fmt.Errorf("native context-preserving compaction is unavailable for %s; use rotate", pendingAction.agentType)
			}
		}
		if err != nil {
			slog.Warn("expired pending rotation batch rejected",
				"agent", pendingAction.snapshot.AgentID,
				"action", pendingAction.action,
				"error", err,
			)
			return
		}
	}

	committedActions := make([]expiredPendingAction, 0, len(actions))
	r.mu.Lock()
	for _, pendingAction := range actions {
		pending := r.pending[pendingAction.snapshot.AgentID]
		if pending == nil ||
			pending != pendingAction.source ||
			pending.ExecutionState != "" ||
			!pending.TimeoutAt.Equal(pendingAction.snapshot.TimeoutAt) ||
			pending.DefaultAction != pendingAction.action ||
			!now.After(pending.TimeoutAt) {
			continue
		}
		switch pendingAction.action {
		case ConfirmPostpone:
			pending.TimeoutAt = now.Add(30 * time.Minute)
			postponed = append(postponed, clonePendingRotation(pending))
		default:
			delete(r.pending, pendingAction.snapshot.AgentID)
			committedActions = append(committedActions, pendingAction)
		}
	}
	r.mu.Unlock()

	for _, pending := range postponed {
		if err := AddPendingRotation(pending); err != nil {
			slog.Warn("failed to persist postponed rotation", "agent", pending.AgentID, "error", err)
		}
	}

	for _, action := range committedActions {
		pending := action.snapshot
		switch action.action {
		case ConfirmRotate:
			result := r.rotateAgent(pending.SessionName, pending.AgentID, pending.WorkDir)
			if !result.Success {
				slog.Warn("auto-rotation from expired pending failed", "agent", pending.AgentID, "error", result.Error)
			}
		case ConfirmCompact:
			if paneID := pending.PaneID; paneID != "" {
				r.tryCompaction(pending.AgentID, paneID, action.agentType)
			}
		case ConfirmIgnore:
			// Do nothing, just remove from pending
		case ConfirmPostpone:
			continue
		}

		if err := RemovePendingRotation(pending.AgentID); err != nil {
			slog.Warn("failed to remove pending rotation from store", "agent", pending.AgentID, "error", err)
		}
	}
}

// rotateAgent performs the full rotation flow for a single agent.
// method specifies why the rotation was triggered (threshold, manual, etc.).
func (r *Rotator) rotateAgent(session, agentID, workDir string, method ...RotationMethod) (result RotationResult) {
	return r.rotateAgentContext(stdcontext.Background(), session, agentID, workDir, method...)
}

func (r *Rotator) rotateAgentContext(ctx stdcontext.Context, session, agentID, workDir string, method ...RotationMethod) (result RotationResult) {
	startTime := time.Now()
	m := RotationThresholdExceeded
	if len(method) > 0 {
		m = method[0]
	}
	result = RotationResult{
		OldAgentID: agentID,
		Method:     m,
		State:      RotationStateInProgress,
		Timestamp:  startTime,
	}
	if ctx == nil {
		result.State = RotationStateFailed
		result.Error = "rotation context is required"
		return result
	}
	if err := ctx.Err(); err != nil {
		result.State = RotationStateFailed
		result.Error = err.Error()
		return result
	}
	contextUsage := 0.0
	defer func() {
		data := alerts.RotationAlertData{
			AgentID:       result.OldAgentID,
			OldAgentID:    result.OldAgentID,
			NewAgentID:    result.NewAgentID,
			Session:       session,
			Pane:          result.OldPaneID,
			ContextUsage:  contextUsage,
			SummaryTokens: result.SummaryTokens,
			DurationMs:    result.Duration.Milliseconds(),
			Error:         result.Error,
		}

		switch result.State {
		case RotationStateCompleted:
			alerts.EmitRotationComplete(data)
		case RotationStateFailed:
			alerts.EmitRotationFailed(data)
		}
	}()

	// Get agent state
	state := r.monitor.GetState(agentID)
	if state == nil {
		result.Success = false
		result.State = RotationStateFailed
		result.Error = "agent not found in monitor"
		result.Duration = time.Since(startTime)
		recordRotationToHistory(result, session, deriveAgentTypeFromID(agentID), 0)
		return result
	}
	if state.Estimate != nil {
		contextUsage = state.Estimate.UsagePercent
	}

	// Find the pane for this agent
	var panes []tmux.Pane
	var err error
	if transport, ok := r.spawner.(rotationContextTransport); ok {
		panes, err = transport.GetPanesContext(ctx, session)
	} else {
		panes, err = r.spawner.GetPanes(session)
	}
	if err != nil {
		result.Success = false
		result.State = RotationStateFailed
		result.Error = fmt.Sprintf("failed to get panes: %v", err)
		result.Duration = time.Since(startTime)
		contextBefore := float64(0)
		if state.Estimate != nil {
			contextBefore = state.Estimate.UsagePercent
		}
		recordRotationToHistory(result, session, deriveAgentTypeFromID(agentID), contextBefore)
		return result
	}

	oldPaneValue, paneErr := findLiveAgentPane(panes, agentID, state.PaneID)
	if paneErr != nil {
		result.Success = false
		result.State = RotationStateFailed
		result.Error = "pane not found for agent"
		result.Duration = time.Since(startTime)
		contextBefore := float64(0)
		if state.Estimate != nil {
			contextBefore = state.Estimate.UsagePercent
		}
		recordRotationToHistory(result, session, deriveAgentTypeFromID(agentID), contextBefore)
		return result
	}
	oldPane := &oldPaneValue
	result.OldPaneID = oldPane.ID
	if r.expectedSource != nil && (oldPane.ID != r.expectedSource.ID || oldPane.PID != r.expectedSource.PID || oldPane.Type.Canonical() != r.expectedSource.Type.Canonical()) {
		result.State = RotationStateFailed
		result.Error = "original agent process identity changed since confirmation"
		result.Duration = time.Since(startTime)
		return result
	}
	if _, observedLifecycle := r.spawner.(rotationPaneLifecycle); observedLifecycle &&
		(oldPane.PID <= 0 || oldPane.Dead || oldPane.IsServicePane() || !rotationProcessRunning(*oldPane)) {
		result.State = RotationStateFailed
		result.Error = "original agent is not a live agent process; refusing to type a summary request"
		result.Duration = time.Since(startTime)
		recordRotationToHistory(result, session, agentTypeLong(string(oldPane.Type)), contextUsage)
		return result
	}
	if err := validateAutomatedRotation(oldPane.Type); err != nil {
		result.Success = false
		result.State = RotationStateFailed
		result.Error = fmt.Sprintf("rotation is unavailable for %s: %v", oldPane.Type.Canonical(), err)
		result.Duration = time.Since(startTime)
		contextBefore := float64(0)
		if state.Estimate != nil {
			contextBefore = state.Estimate.UsagePercent
		}
		recordRotationToHistory(result, session, agentTypeLong(string(oldPane.Type)), contextBefore)
		return result
	}

	// Try compaction first if configured
	if r.config.TryCompactFirst && r.compactor != nil {
		compactResult := r.tryCompactionContext(ctx, session, agentID, oldPane.ID, oldPane.Type)
		if compactResult != nil && compactResult.Success {
			// Check if we're now below threshold
			if compactResult.UsageAfter < r.config.RotateThreshold*100 {
				// Compaction worked, no rotation needed
				result.Success = true
				result.State = RotationStateAborted
				result.Error = "compaction succeeded, rotation not needed"
				result.Duration = time.Since(startTime)
				return result
			}
		}
		// Compaction didn't help enough, proceed with rotation
		result.Method = RotationCompactionFailed
	}

	alerts.EmitRotationStarted(alerts.RotationAlertData{
		AgentID:      agentID,
		Session:      session,
		Pane:         oldPane.ID,
		ContextUsage: contextUsage,
	})

	// Request handoff summary from the old agent
	summaryPrompt := r.summary.GeneratePrompt()
	transport, observeSource := r.spawner.(rotationContextTransport)
	startMarker, endMarker := "", ""
	if observeSource {
		summaryPrompt, startMarker, endMarker = rotationSummaryRequest(r.summary)
		_, err = waitForRotationReady(ctx, session, *oldPane, rotationReadyTimeout, rotationReadyPoll,
			transport.GetPanesContext, tmux.CapturePaneVisibleContext)
	}
	if err == nil {
		err = sendRotationPromptContext(ctx, r.spawner, oldPane.ID, summaryPrompt)
	}
	if err != nil {
		result.Success = false
		result.State = RotationStateFailed
		result.Error = fmt.Sprintf("failed to request summary: %v", err)
		result.Duration = time.Since(startTime)
		contextBefore := float64(0)
		if state.Estimate != nil {
			contextBefore = state.Estimate.UsagePercent
		}
		recordRotationToHistory(result, session, agentTypeLong(string(oldPane.Type)), contextBefore)
		return result
	}

	agentTypeName := agentTypeLong(string(oldPane.Type))
	var handoffSummary *HandoffSummary
	if observeSource {
		handoffSummary, err = waitForRotationSummary(ctx, session, agentID, *oldPane, r.summary, startMarker, endMarker, rotationReadyPoll,
			transport.GetPanesContext, tmux.CapturePaneOutputContext, tmux.CapturePaneVisibleContext)
		if err != nil {
			result.State = RotationStateFailed
			result.Error = fmt.Sprintf("waiting for handoff summary; original agent preserved: %v", err)
			result.Duration = time.Since(startTime)
			recordRotationToHistory(result, session, agentTypeName, contextUsage)
			return result
		}
	} else {
		// An external PaneSpawner owns its summary/input protocol. Retain its
		// existing capture path without imposing tmux-only process observations.
		if err := waitForRotationDelay(ctx, 5*time.Second); err != nil {
			result.State = RotationStateFailed
			result.Error = fmt.Sprintf("waiting for handoff summary; original agent preserved: %v", err)
			result.Duration = time.Since(startTime)
			recordRotationToHistory(result, session, agentTypeName, contextUsage)
			return result
		}
		summaryText, captureErr := tmux.CapturePaneOutputContext(ctx, oldPane.ID, 100)
		if captureErr == nil && strings.TrimSpace(summaryText) != "" {
			handoffSummary = r.summary.ParseAgentResponse(agentID, agentTypeName, session, summaryText)
		} else {
			handoffSummary = r.summary.GenerateFallbackSummary(agentID, agentTypeName, session, []string{summaryText})
		}
	}
	if handoffSummary != nil {
		result.SummaryTokens = handoffSummary.TokenEstimate
	}

	// Spawn replacement agent with same type
	agentType := agentTypeLong(string(oldPane.Type))
	newIndex := extractAgentIndex(agentID)
	lifecycle, hasLifecycle := r.spawner.(rotationPaneLifecycle)
	var replacement tmux.Pane
	if err = ctx.Err(); err == nil {
		if hasLifecycle {
			replacement, err = lifecycle.SpawnReplacementContext(ctx, session, *oldPane, newIndex, workDir)
		} else {
			replacement.ID, err = r.spawner.SpawnAgent(session, agentType, newIndex, oldPane.Variant, workDir)
		}
	}
	newPaneID := replacement.ID
	result.NewPaneID = newPaneID
	if err != nil {
		result.Success = false
		result.State = RotationStateFailed
		result.Error = fmt.Sprintf("failed to spawn replacement: %v", err)
		result.Duration = time.Since(startTime)
		contextBefore := float64(0)
		if state.Estimate != nil {
			contextBefore = state.Estimate.UsagePercent
		}
		recordRotationToHistory(result, session, agentType, contextBefore)
		return result
	}
	result.NewAgentID = tmux.FormatPaneName(session, agentTypeShort(agentType), newIndex, oldPane.Variant)

	// Deliver the handoff before retiring the original agent or replacing its
	// monitor state. A live replacement without the task context is not a
	// successful rotation: the original pane remains the recovery source.
	if handoffSummary != nil {
		handoffContext := handoffSummary.FormatForNewAgent()
		if hasLifecycle {
			err = lifecycle.DeliverHandoffContext(ctx, session, replacement, handoffContext)
		} else {
			err = sendRotationPromptContext(ctx, r.spawner, newPaneID, handoffContext)
		}
		if err != nil {
			result.State = RotationStateFailed
			result.Error = fmt.Sprintf("failed to send handoff context; original agent preserved: %v", err)
			var cleanupErr error
			if hasLifecycle {
				cleanupCtx, cancel := stdcontext.WithTimeout(stdcontext.WithoutCancel(ctx), 5*time.Second)
				cleanupErr = lifecycle.KillRotationPaneContext(cleanupCtx, session, replacement)
				cancel()
			} else {
				cleanupErr = r.spawner.KillPane(newPaneID)
			}
			if cleanupErr != nil {
				result.Error += fmt.Sprintf("; failed to remove replacement pane %s: %v", newPaneID, cleanupErr)
			}
			result.Duration = time.Since(startTime)
			recordRotationToHistory(result, session, agentType, contextUsage)
			return result
		}
	}

	// The replacement begins with an empty context window. Only now commit the
	// monitor transition, so failed handoffs retain the predecessor's usage and
	// remain eligible for a later retry. Custom titles can produce a different
	// canonical replacement ID.
	if result.NewAgentID != agentID {
		r.monitor.UnregisterAgent(agentID)
	}
	r.monitor.RegisterAgent(result.NewAgentID, newPaneID, state.Model)
	r.monitor.ResetAgent(result.NewAgentID)

	// Kill the old pane
	if hasLifecycle {
		err = lifecycle.KillRotationPaneContext(ctx, session, *oldPane)
	} else {
		err = r.spawner.KillPane(oldPane.ID)
	}
	if err != nil {
		// Non-fatal: new agent is running
		if result.Error != "" {
			result.Error += "; "
		}
		result.Error += fmt.Sprintf("warning: failed to kill old pane: %v", err)
	}

	// Record the rotation event
	contextBefore := float64(0)
	if state.Estimate != nil {
		contextBefore = state.Estimate.UsagePercent
	}
	event := RotationEvent{
		SessionName:   session,
		OldAgentID:    agentID,
		NewAgentID:    result.NewAgentID,
		AgentType:     agentType,
		Method:        result.Method,
		ContextBefore: contextBefore,
		ContextAfter:  0, // Fresh agent
		SummaryTokens: result.SummaryTokens,
		Duration:      time.Since(startTime),
		Timestamp:     startTime,
	}
	r.mu.Lock()
	r.history = append(r.history, event)
	r.mu.Unlock()

	result.Success = true
	result.State = RotationStateCompleted
	result.Duration = time.Since(startTime)

	// Record to persistent history (for audit log)
	recordRotationToHistory(result, session, agentType, contextBefore)

	return result
}

// recordRotationToHistory persists a rotation result to the audit log.
// This is best-effort; history write failures don't affect the rotation result.
func recordRotationToHistory(result RotationResult, session, agentType string, contextBefore float64) {
	historyRecord := &RotationRecord{
		ID:               newRecordID(),
		Timestamp:        result.Timestamp,
		SessionName:      session,
		AgentID:          result.OldAgentID,
		AgentType:        agentType,
		ContextBefore:    contextBefore,
		EstimationMethod: "token_count",
		Method:           result.Method,
		Success:          result.Success,
		SummaryTokens:    result.SummaryTokens,
		ContextAfter:     0,
		DurationMs:       result.Duration.Milliseconds(),
	}
	if !result.Success {
		historyRecord.FailureReason = result.Error
	}
	// Best-effort persist - don't fail rotation if history write fails
	if err := RecordRotation(historyRecord); err != nil {
		slog.Warn("failed to persist rotation history", "agent", result.OldAgentID, "error", err)
	}
}

// tryCompaction attempts to compact the agent's context.
func (r *Rotator) tryCompaction(agentID, paneID string, agentType agent.AgentType) *CompactionResult {
	return r.tryCompactionContext(stdcontext.Background(), "", agentID, paneID, agentType)
}

// compactLivePane obtains fresh provider accounting rather than comparing the
// same cached monitor estimate before and after a command. Unsupported or
// ambiguous accounting never authorizes clearing the original conversation.
func (r *Rotator) compactLivePane(ctx stdcontext.Context, session, agentID, paneID string, agentType agent.AgentType, transport rotationContextTransport) *CompactionResult {
	commands := r.compactor.GetCompactionCommands(string(agentType))
	if len(commands) == 0 {
		return &CompactionResult{Method: CompactionFailed, Error: "native context-preserving compaction is unavailable for this provider; use rotate"}
	}
	panes, err := transport.GetPanesContext(ctx, session)
	if err != nil {
		return &CompactionResult{Method: CompactionFailed, Error: err.Error()}
	}
	pane, err := findLiveAgentPane(panes, agentID, paneID)
	if err != nil {
		return &CompactionResult{Method: CompactionFailed, Error: err.Error()}
	}
	if r.expectedSource != nil && (pane.ID != r.expectedSource.ID || pane.PID != r.expectedSource.PID || pane.Type.Canonical() != r.expectedSource.Type.Canonical()) {
		return &CompactionResult{Method: CompactionFailed, Error: "original agent process identity changed since confirmation"}
	}
	usage := func(ctx stdcontext.Context, captured string, before *TranscriptUsage) (*TranscriptUsage, error) {
		if pane.Type.Canonical() == agent.AgentTypeOmp {
			if reading, ok := OmpStatusBarUsage(captured, time.Now()); ok {
				return reading, nil
			}
			return nil, errors.New("agent context gauge is unavailable")
		}
		if tmux.DefaultClient.Remote != "" {
			return nil, errors.New("local transcript cannot verify a remote agent's compaction")
		}
		if before != nil {
			reading, err := ReadLatestTranscriptUsage(before.Path)
			if err != nil || reading == nil {
				return nil, errors.New("original transcript accounting is unavailable")
			}
			return reading, nil
		}
		cwd, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.ExactTarget(pane.ID), "#{pane_current_path}")
		cwd = strings.TrimSpace(cwd)
		if err != nil || !filepath.IsAbs(cwd) {
			return nil, errors.New("original agent working directory is unavailable")
		}
		// The provider's project transcript directory spans every tmux session.
		// A session-local pane list cannot rule out another conversation in it.
		allPanes, err := tmux.GetAllPanesContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("cannot establish server-wide transcript attribution: %w", err)
		}
		foundSource := false
		for siblingSession, siblings := range allPanes {
			for _, sibling := range siblings {
				if sibling.ID == pane.ID {
					if siblingSession != session || sibling.PID != pane.PID || sibling.Type.Canonical() != pane.Type.Canonical() || sibling.Dead || sibling.IsServicePane() {
						return nil, errors.New("original agent identity changed during transcript attribution")
					}
					foundSource = true
					continue
				}
				if sibling.Type.Canonical() != pane.Type.Canonical() || sibling.IsServicePane() {
					continue
				}
				other, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.ExactTarget(sibling.ID), "#{pane_current_path}")
				other = strings.TrimSpace(other)
				if err != nil || !filepath.IsAbs(other) || MungeProjectPath(other) == MungeProjectPath(cwd) {
					return nil, errors.New("provider transcript attribution is ambiguous across tmux sessions; use rotate")
				}
			}
		}
		if !foundSource {
			return nil, errors.New("original agent is absent from the server-wide pane inventory")
		}
		reading, ok := LatestAgentTranscriptUsage(agentTypeLong(string(pane.Type)), cwd, time.Time{})
		if !ok || reading == nil || time.Since(reading.UpdatedAt) > TranscriptFreshness {
			return nil, errors.New("fresh provider context accounting is unavailable; use rotate")
		}
		return reading, nil
	}
	timeout := r.compactor.builtinTimeout
	if timeout < rotationReadyTimeout {
		timeout = rotationReadyTimeout
	}
	return runNativeCompaction(ctx, session, pane, commands[0], r.compactor, timeout, rotationReadyPoll,
		transport.GetPanesContext, tmux.CapturePaneVisibleContext, transport.SendKeysContext, usage)
}

type rotationUsageReader func(stdcontext.Context, string, *TranscriptUsage) (*TranscriptUsage, error)

func runNativeCompaction(ctx stdcontext.Context, session string, expected tmux.Pane, command CompactionCommand, compactor *Compactor, timeout, poll time.Duration,
	list rotationPaneLister, capture rotationPaneCapture, send func(stdcontext.Context, string, string, bool) error, usage rotationUsageReader) *CompactionResult {
	started := time.Now()
	fail := func(err error) *CompactionResult {
		return &CompactionResult{Method: CompactionFailed, Error: err.Error(), Duration: time.Since(started)}
	}
	if ctx == nil || compactor == nil || list == nil || capture == nil || send == nil || usage == nil {
		return fail(errors.New("compaction requires a context and observation dependencies"))
	}
	waitCtx, cancel := stdcontext.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := waitForRotationReady(waitCtx, session, expected, timeout, poll, list, capture); err != nil {
		return fail(err)
	}
	captured, err := capture(waitCtx, expected.ID)
	if err != nil {
		return fail(err)
	}
	before, err := usage(waitCtx, captured, nil)
	if err != nil || before == nil {
		return fail(fmt.Errorf("cannot establish pre-compaction usage: %v", err))
	}
	limit := int64(before.ContextWindow)
	if limit <= 0 {
		limit = GetContextLimit(before.Model)
	}
	if limit <= 0 || before.Tokens <= 0 {
		return fail(errors.New("provider did not report usable context accounting"))
	}
	// Reobserve immediately before typing: usage discovery can take time.
	pane, err := currentRotationPane(waitCtx, session, expected, list)
	if err != nil {
		return fail(err)
	}
	captured, err = capture(waitCtx, expected.ID)
	if err != nil {
		return fail(err)
	}
	if ready, reason := rotationPromptReady(captured, pane); !ready || !rotationProcessRunning(pane) {
		return fail(fmt.Errorf("original agent is not ready for compaction: %s", reason))
	}
	if _, err := currentRotationPane(waitCtx, session, expected, list); err != nil {
		return fail(err)
	}
	if err := send(waitCtx, expected.ID, command.Command, true); err != nil {
		return fail(fmt.Errorf("native compaction submission failed: %w", err))
	}
	stable := 0
	for {
		if err := waitCtx.Err(); err != nil {
			return fail(fmt.Errorf("native compaction did not produce fresh completed usage: %w", err))
		}
		pane, err := currentRotationPane(waitCtx, session, expected, list)
		if err != nil {
			return fail(err)
		}
		if !rotationProcessRunning(pane) {
			return fail(errors.New("original agent exited during compaction"))
		}
		captured, err := capture(waitCtx, expected.ID)
		if err != nil {
			return fail(err)
		}
		ready, _ := rotationPromptReady(captured, pane)
		after, readErr := usage(waitCtx, captured, before)
		fresh := readErr == nil && after != nil && after.Path == before.Path && after.Model == before.Model &&
			after.ContextWindow == before.ContextWindow && after.UpdatedAt.After(before.UpdatedAt) && after.Tokens != before.Tokens
		if ready && fresh {
			stable++
			if stable >= 2 {
				if _, err := currentRotationPane(waitCtx, session, expected, list); err != nil {
					return fail(err)
				}
				result := compactor.EvaluateCompactionResult(
					&ContextEstimate{TokensUsed: int64(before.Tokens), UsagePercent: float64(before.Tokens) / float64(limit) * 100},
					&ContextEstimate{TokensUsed: int64(after.Tokens), UsagePercent: float64(after.Tokens) / float64(limit) * 100})
				result.Method, result.Duration = CompactionBuiltin, time.Since(started)
				return result
			}
		} else {
			stable = 0
		}
		if err := waitForRotationDelay(waitCtx, poll); err != nil {
			return fail(fmt.Errorf("native compaction did not produce fresh completed usage: %w", err))
		}
	}
}

func (r *Rotator) tryCompactionContext(ctx stdcontext.Context, session, agentID, paneID string, agentType agent.AgentType) *CompactionResult {
	if r.compactor == nil {
		return nil
	}
	if r.spawner == nil {
		return &CompactionResult{Success: false, Method: CompactionFailed, Error: "no spawner available"}
	}
	if transport, ok := r.spawner.(rotationContextTransport); ok {
		return r.compactLivePane(ctx, session, agentID, paneID, agentType, transport)
	}

	// Start compaction state
	state, err := r.compactor.NewCompactionState(agentID)
	if err != nil {
		return &CompactionResult{Success: false, Method: CompactionFailed, Error: err.Error()}
	}

	cmds := r.compactor.GetCompactionCommands(agentTypeLong(string(agentType.Canonical())))
	if len(cmds) == 0 {
		return &CompactionResult{Success: false, Method: CompactionFailed, Error: "no compaction commands available"}
	}

	for _, cmd := range cmds {
		if err := ctx.Err(); err != nil {
			return &CompactionResult{Success: false, Method: CompactionFailed, Error: err.Error()}
		}
		// Both slash commands and prompts need enter=true to be submitted.
		if transport, ok := r.spawner.(rotationContextTransport); ok {
			if cmd.IsPrompt {
				err = transport.SendBufferContext(ctx, paneID, cmd.Command, true)
			} else {
				err = transport.SendKeysContext(ctx, paneID, cmd.Command, true)
			}
		} else {
			err = sendCompactionCommandToPane(r.spawner, paneID, cmd)
		}
		if err != nil {
			slog.Error("failed to send compaction command", "pane_id", paneID, "error", err)
			continue
		}

		state.UpdateState(cmd, compactionMethodForCommand(cmd))

		// Wait for compaction to complete.
		if err := waitForRotationDelay(ctx, cmd.WaitTime); err != nil {
			return &CompactionResult{Success: false, Method: CompactionFailed, Error: err.Error()}
		}

		// Finish and evaluate.
		result, err := r.compactor.FinishCompaction(state)
		if err != nil {
			slog.Warn("compaction finish failed", "error", err)
			continue
		}
		if result.Success {
			return result
		}

		slog.Info("compaction method did not achieve target reduction, trying next",
			"method", result.Method,
			"error", result.Error,
		)
	}

	return &CompactionResult{Success: false, Method: CompactionFailed, Error: "all compaction methods exhausted"}
}

// extractAgentIndex extracts the numeric index from an agent ID.
// e.g., "myproject__cc_2" -> 2
func extractAgentIndex(agentID string) int {
	// Read only the type_index part. Models and persona names can contain
	// numbers and underscores; scanning from the end can change agent 1 into
	// agent 2026 when its model variant contains a release date.
	suffix := tmux.PaneTitleSuffix(agentID)
	if suffix == "" {
		suffix = agentID
	}
	parts := strings.SplitN(suffix, "_", 3)
	if len(parts) >= 2 {
		if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

// deriveAgentTypeFromID extracts agent type from agent ID.
// e.g., "myproject__cc_2" -> "claude", "myproject__cod_1" -> "codex"
func deriveAgentTypeFromID(agentID string) string {
	// Format: session__type_index
	typePart := tmux.PaneTitleSuffix(agentID)
	if typePart == "" {
		return "unknown"
	}
	// typePart is like "cc_2" or "cod_1_variant"
	typeParts := strings.Split(typePart, "_")
	// strings.Split always returns at least one element, so typeParts[0] is safe
	return agentTypeLong(typeParts[0])
}

// GetHistory returns the rotation history.
func (r *Rotator) GetHistory() []RotationEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RotationEvent, len(r.history))
	copy(out, r.history)
	return out
}

// ClearHistory clears the rotation history.
func (r *Rotator) ClearHistory() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.history = make([]RotationEvent, 0)
}

// NeedsRotation checks if any agent needs rotation.
// Returns agent IDs that need rotation and a reason string.
func (r *Rotator) NeedsRotation() ([]string, string) {
	if r.monitor == nil {
		return nil, "no monitor available"
	}
	if !r.config.Enabled {
		return nil, "rotation disabled"
	}

	agentInfos := r.monitor.AgentsAboveThreshold(r.config.RotateThreshold * 100)
	if len(agentInfos) == 0 {
		return nil, "no agents above threshold"
	}

	agentIDs := make([]string, len(agentInfos))
	for i, info := range agentInfos {
		agentIDs[i] = info.AgentID
	}

	return agentIDs, fmt.Sprintf("%d agent(s) above %.0f%% threshold",
		len(agentIDs), r.config.RotateThreshold*100)
}

// NeedsWarning checks if any agent is above the warning threshold.
// Returns agent IDs that need warning and a reason string.
func (r *Rotator) NeedsWarning() ([]string, string) {
	if r.monitor == nil {
		return nil, "no monitor available"
	}
	if !r.config.Enabled {
		return nil, "rotation disabled"
	}

	agentInfos := r.monitor.AgentsAboveThreshold(r.config.WarningThreshold * 100)
	if len(agentInfos) == 0 {
		return nil, "no agents above warning threshold"
	}

	agentIDs := make([]string, len(agentInfos))
	for i, info := range agentInfos {
		agentIDs[i] = info.AgentID
	}

	return agentIDs, fmt.Sprintf("%d agent(s) above %.0f%% warning threshold",
		len(agentInfos), r.config.WarningThreshold*100)
}

// ManualRotate triggers a rotation for a specific agent regardless of threshold.
func (r *Rotator) ManualRotate(session, agentID, workDir string) RotationResult {
	// Check prerequisites that rotateAgent assumes
	if r.monitor == nil {
		return RotationResult{
			Success:    false,
			OldAgentID: agentID,
			Method:     RotationManual,
			State:      RotationStateFailed,
			Error:      "no monitor available",
			Timestamp:  time.Now(),
		}
	}
	if r.spawner == nil {
		return RotationResult{
			Success:    false,
			OldAgentID: agentID,
			Method:     RotationManual,
			State:      RotationStateFailed,
			Error:      "no spawner available",
			Timestamp:  time.Now(),
		}
	}

	return r.rotateAgent(session, agentID, workDir, RotationManual)
}

// FormatRotationResult formats a rotation result for display.
func (r *RotationResult) FormatForDisplay() string {
	var sb strings.Builder

	if r.Success {
		sb.WriteString("✓ Rotation completed\n")
	} else {
		sb.WriteString("✗ Rotation failed\n")
	}

	sb.WriteString(fmt.Sprintf("  Old Agent: %s\n", r.OldAgentID))
	if r.NewAgentID != "" {
		sb.WriteString(fmt.Sprintf("  New Agent: %s\n", r.NewAgentID))
	}
	sb.WriteString(fmt.Sprintf("  Method: %s\n", r.Method))
	sb.WriteString(fmt.Sprintf("  State: %s\n", r.State))
	if r.SummaryTokens > 0 {
		sb.WriteString(fmt.Sprintf("  Summary Tokens: %d\n", r.SummaryTokens))
	}
	sb.WriteString(fmt.Sprintf("  Duration: %s\n", r.Duration.Round(time.Millisecond)))

	if r.Error != "" {
		sb.WriteString(fmt.Sprintf("  Error: %s\n", r.Error))
	}

	return sb.String()
}

// GetPendingRotations returns all pending rotations awaiting confirmation.
func (r *Rotator) GetPendingRotations() []*PendingRotation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*PendingRotation, 0, len(r.pending))
	for _, p := range r.pending {
		result = append(result, p)
	}
	return result
}

// GetPendingRotation returns a specific pending rotation by agent ID.
func (r *Rotator) GetPendingRotation(agentID string) *PendingRotation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pending[agentID]
}

// HasPendingRotation returns true if there is a pending rotation for the agent.
func (r *Rotator) HasPendingRotation(agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.pending[agentID]
	return exists
}

// EnqueuePendingRotation registers a pending rotation for an agent through the
// same machinery CheckAndRotate uses when RequireConfirm is set: the rotation
// is tracked in-memory (so ConfirmRotation can execute it) and persisted to
// the pending rotation store consumed by `ntm rotate context pending/confirm`.
// It is used by external triggers — such as the coordinator's
// transcript-usage threshold check (bd-rpmg8) — that decide eligibility
// themselves. If a pending rotation already exists for the agent, it is
// returned unchanged.
func (r *Rotator) EnqueuePendingRotation(session, agentID, paneID string, contextPercent float64, workDir string) *PendingRotation {
	r.mu.RLock()
	existing := r.pending[agentID]
	r.mu.RUnlock()
	if existing != nil && !existing.IsExpired() {
		return existing
	}
	// No pending rotation, or only an expired leftover: create a fresh one
	// (createPendingRotation overwrites both the in-memory entry and the
	// persisted store entry).
	return r.createPendingRotation(session, agentID, paneID, contextPercent, workDir)
}

// ConfirmRotation handles user confirmation of a pending rotation.
// Returns the result of the action taken.
func (r *Rotator) ConfirmRotation(agentID string, action ConfirmAction, postponeMinutes int) RotationResult {
	return r.ConfirmRotationContext(stdcontext.Background(), agentID, action, postponeMinutes)
}

// ConfirmPendingRotationContext executes a persisted choice through the same
// lifecycle as coordinator rotation. The durable claim spans all effects and
// stores the actual result, so a reported success can be replayed safely.
func ConfirmPendingRotationContext(ctx stdcontext.Context, agentID string, action ConfirmAction, minutes int, retry bool, cfg *config.Config) RotationResult {
	return confirmStoredRotation(ctx, agentID, action, minutes, retry, cfg, nil, false)
}

func confirmStoredRotation(ctx stdcontext.Context, agentID string, action ConfirmAction, minutes int, retry bool, cfg *config.Config, owner *Rotator, allowExpired bool) (result RotationResult) {
	started := time.Now()
	result = RotationResult{OldAgentID: agentID, State: RotationStateFailed, Timestamp: started}
	if action == ConfirmPostpone && minutes <= 0 {
		result.Error = "postpone minutes must be positive"
		return result
	}
	var pending *PendingRotation
	var release func()
	var err error
	if allowExpired {
		pending, release, err = DefaultPendingRotationStore.BeginExpiredConfirmation(ctx, agentID, action)
	} else {
		pending, release, err = DefaultPendingRotationStore.BeginConfirmation(ctx, agentID, action, retry)
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer release()
	if pending.ExecutionState == RotationStateCompleted && pending.Result != nil {
		if owner != nil {
			owner.mu.Lock()
			delete(owner.pending, agentID)
			owner.mu.Unlock()
		}
		return *pending.Result
	}
	result.OldPaneID = pending.PaneID
	defer func() {
		result.Duration = time.Since(started)
		if err := DefaultPendingRotationStore.FinishConfirmation(ctx, pending, result); err != nil {
			result.Success = false
			result.State = RotationStateFailed
			result.Error += fmt.Sprintf("; confirmation outcome could not be persisted: %v", err)
		}
		if owner != nil {
			owner.mu.Lock()
			if result.Success && action != ConfirmPostpone {
				delete(owner.pending, agentID)
			} else {
				retained := clonePendingRotation(pending)
				if action == ConfirmPostpone && result.Success {
					retained.SelectedAction, retained.ExecutionState, retained.ExecutionID, retained.Result = "", "", "", nil
				} else {
					retained.ExecutionState = RotationStateFailed
					copyResult := result
					retained.Result = &copyResult
				}
				owner.pending[agentID] = retained
			}
			owner.mu.Unlock()
		}
	}()
	if err := ctx.Err(); err != nil {
		result.Error = err.Error()
		return result
	}
	// Administrative choices do not need a live provider or physical pane.
	if action == ConfirmIgnore {
		result.Success, result.State = true, RotationStateAborted
		return result
	}
	if action == ConfirmPostpone {
		pending.TimeoutAt = time.Now().Add(time.Duration(minutes) * time.Minute)
		result.Success, result.State = true, RotationStatePending
		return result
	}
	if pending.Remote != tmux.DefaultClient.Remote {
		result.Error = "pending rotation belongs to a different tmux host; use the same --ssh target that enqueued it"
		return result
	}
	if pending.PaneID == "" || pending.PanePID <= 0 || pending.PaneType == "" {
		result.Error = "pending rotation has no recorded pane process identity; acknowledge it with --action=ignore --retry, then let the coordinator create a fresh request"
		return result
	}
	if err := tmux.ValidateSessionName(pending.SessionName); err != nil {
		result.Error = fmt.Sprintf("invalid pending session: %v", err)
		return result
	}
	expected := tmux.Pane{ID: pending.PaneID, PID: pending.PanePID, Type: tmux.AgentType(pending.PaneType)}
	if err := validateAutomatedRotation(expected.Type); err != nil {
		result.Error = err.Error()
		return result
	}
	observed, err := currentRotationPane(ctx, pending.SessionName, expected, tmux.GetPanesContext)
	if err != nil {
		result.Error = fmt.Sprintf("pending source identity cannot be verified: %v", err)
		return result
	}
	if !rotationProcessRunning(observed) {
		result.Error = "pending source is no longer a running agent"
		return result
	}
	captured, err := tmux.CapturePaneVisibleContext(ctx, observed.ID)
	if err != nil {
		result.Error = fmt.Sprintf("cannot observe pending source: %v", err)
		return result
	}
	if ready, reason := rotationPromptReady(captured, observed); !ready {
		result.Error = "pending source is not ready: " + reason
		return result
	}
	if cfg == nil {
		cfg = config.Default()
	}
	monitor := NewContextMonitor(DefaultMonitorConfig())
	rotCfg := RotatorConfig{Monitor: monitor, Spawner: NewDefaultPaneSpawner(cfg), Config: cfg.ContextRotation}
	if owner != nil {
		rotCfg.Monitor, rotCfg.Compactor, rotCfg.Summary, rotCfg.Spawner, rotCfg.Config = owner.monitor, owner.compactor, owner.summary, owner.spawner, owner.config
		monitor = owner.monitor
	}
	if action == ConfirmRotate && owner == nil && !allowExpired {
		// An explicit CLI/dashboard choice must replace the agent even when
		// automatic rotation prefers compaction. Modify this execution's copy;
		// coordinator auto-confirm and timeout policy keep their preference.
		rotCfg.Config.TryCompactFirst = false
	}
	model := ""
	if state := monitor.GetState(agentID); state != nil {
		model = state.Model
	}
	if spec, err := tmux.ReadPaneLaunchSpecContext(ctx, observed.ID); err != nil {
		result.Error = fmt.Sprintf("cannot read source launch settings: %v", err)
		return result
	} else if spec != nil {
		model = spec.Model
	}
	if monitor.GetState(agentID) == nil {
		monitor.RegisterAgent(agentID, observed.ID, model)
		monitor.SetAgentType(agentID, string(observed.Type.Canonical()))
	}
	runtime := NewRotator(rotCfg)
	runtime.durableConfirmation = true
	runtime.expectedSource = &expected
	runtime.pending[agentID] = clonePendingRotation(pending)
	return runtime.ConfirmRotationContext(ctx, agentID, action, minutes)
}

// ConfirmRotationContext executes the confirmed rotation with caller
// cancellation covering launch, readiness observation and handoff delivery.
func (r *Rotator) ConfirmRotationContext(ctx stdcontext.Context, agentID string, action ConfirmAction, postponeMinutes int) (result RotationResult) {
	if ctx == nil || ctx.Err() != nil {
		err := errors.New("rotation context is required")
		if ctx != nil {
			err = ctx.Err()
		}
		return RotationResult{OldAgentID: agentID, State: RotationStateFailed, Error: err.Error(), Timestamp: time.Now()}
	}
	// All production confirmations, including coordinator auto-confirm, own
	// the same persisted claim. A CLI claim must never race a second engine.
	if spawner, ok := r.spawner.(*DefaultPaneSpawner); ok && !r.durableConfirmation {
		return confirmStoredRotation(ctx, agentID, action, postponeMinutes, false, spawner.config, r, false)
	}
	r.mu.Lock()
	pending := r.pending[agentID]
	if pending == nil {
		r.mu.Unlock()
		return RotationResult{
			OldAgentID: agentID,
			State:      RotationStateFailed,
			Error:      "no pending rotation found for agent",
			Timestamp:  time.Now(),
		}
	}
	pendingCopy := clonePendingRotation(pending)
	if r.confirming[agentID] {
		r.mu.Unlock()
		return RotationResult{OldAgentID: agentID, State: RotationStateFailed, Error: "confirmation is already executing", Timestamp: time.Now()}
	}
	r.confirming[agentID] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.confirming, agentID)
		if result.Success && action != ConfirmPostpone {
			delete(r.pending, agentID)
		}
		r.mu.Unlock()
		if result.Success && !r.durableConfirmation {
			var err error
			if action == ConfirmPostpone {
				err = AddPendingRotation(pendingCopy)
			} else {
				err = RemovePendingRotation(agentID)
			}
			if err != nil {
				result.Success = false
				result.State = RotationStateFailed
				result.Error = fmt.Sprintf("action finished but pending state could not be acknowledged: %v", err)
			}
		}
	}()

	result = RotationResult{
		OldAgentID: agentID,
		OldPaneID:  pendingCopy.PaneID,
		Timestamp:  time.Now(),
	}

	switch action {
	case ConfirmRotate:
		agentType, err := r.resolveLiveAgentTypeContext(ctx, pendingCopy.SessionName, agentID, pendingCopy.PaneID)
		if err == nil {
			err = validateAutomatedRotation(agentType)
		}
		if err != nil {
			result.State = RotationStateFailed
			result.Error = err.Error()
			return result
		}
		return r.rotateAgentContext(ctx, pendingCopy.SessionName, agentID, pendingCopy.WorkDir)

	case ConfirmCompact:
		// Try compaction first
		if pendingCopy.PaneID == "" {
			result.State = RotationStateFailed
			result.Error = "cannot compact: pane ID unknown"
			return result
		}
		agentType, err := r.resolveLiveAgentTypeContext(ctx, pendingCopy.SessionName, agentID, pendingCopy.PaneID)
		if err == nil {
			err = agentType.ValidateAutomatedPromptDelivery()
		}
		if err != nil {
			result.State = RotationStateFailed
			result.Error = err.Error()
			return result
		}
		compactResult := r.tryCompactionContext(ctx, pendingCopy.SessionName, agentID, pendingCopy.PaneID, agentType)
		if compactResult != nil && compactResult.Success {
			result.Success = true
			result.State = RotationStateAborted
			result.Error = "compaction succeeded, rotation not needed"
		} else {
			result.State = RotationStateFailed
			result.Error = "compaction failed"
			if compactResult != nil && compactResult.Error != "" {
				result.Error = compactResult.Error
			}
		}
		return result

	case ConfirmIgnore:
		result.Success = true
		result.State = RotationStateAborted
		result.Error = "rotation cancelled by user"
		return result

	case ConfirmPostpone:
		// Extend the timeout
		minutes := postponeMinutes
		if minutes <= 0 {
			minutes = 30 // Default postpone duration
		}
		pendingCopy.TimeoutAt = time.Now().Add(time.Duration(minutes) * time.Minute)
		r.mu.Lock()
		r.pending[agentID] = clonePendingRotation(pendingCopy)
		r.mu.Unlock()
		result.Success = true
		result.State = RotationStatePending
		result.Error = fmt.Sprintf("rotation postponed for %d minutes", minutes)
		return result

	default:
		result.State = RotationStateFailed
		result.Error = fmt.Sprintf("unknown action: %s", action)
		return result
	}
}

// CancelPendingRotation removes a pending rotation without taking any action.
func (r *Rotator) CancelPendingRotation(agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pending[agentID]; exists {
		delete(r.pending, agentID)
		if err := RemovePendingRotation(agentID); err != nil {
			slog.Warn("failed to remove pending rotation from store", "agent", agentID, "error", err)
		}
		return true
	}
	return false
}

// ClearPending removes all pending rotations.
func (r *Rotator) ClearPending() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = make(map[string]*PendingRotation)
	if err := DefaultPendingRotationStore.Clear(); err != nil {
		slog.Warn("failed to clear pending rotation store", "error", err)
	}
}
