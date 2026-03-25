// Package context provides context window monitoring for AI agent orchestration.
// rotation.go implements seamless agent rotation when context window is exhausted.
package context

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
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
	AgentID        string        `json:"agent_id"`
	SessionName    string        `json:"session_name"`
	PaneID         string        `json:"pane_id"`
	ContextPercent float64       `json:"context_percent"`
	CreatedAt      time.Time     `json:"created_at"`
	TimeoutAt      time.Time     `json:"timeout_at"`
	DefaultAction  ConfirmAction `json:"default_action"`
	WorkDir        string        `json:"-"` // Not serialized
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

// NewPendingRotationOutput creates a robot mode output for a pending rotation.
func NewPendingRotationOutput(p *PendingRotation) PendingRotationOutput {
	remaining := int(time.Until(p.TimeoutAt).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return PendingRotationOutput{
		Type:             "rotation_pending",
		AgentID:          p.AgentID,
		SessionName:      p.SessionName,
		ContextPercent:   p.ContextPercent,
		AwaitingConfirm:  true,
		TimeoutSeconds:   remaining,
		DefaultAction:    string(p.DefaultAction),
		AvailableActions: []string{"rotate", "compact", "ignore", "postpone"},
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
	}
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
	return time.Now().After(p.TimeoutAt)
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
	// Create a new pane
	paneID, err := tmux.SplitWindow(session, workDir)
	if err != nil {
		return "", fmt.Errorf("creating pane: %w", err)
	}

	// Set the pane title
	shortType := agentTypeShort(agentType)
	title := tmux.FormatPaneName(session, shortType, index, variant)
	if err := tmux.SetPaneTitle(paneID, title); err != nil {
		// Clean up orphaned pane on failure
		_ = tmux.KillPane(paneID)
		return "", fmt.Errorf("setting pane title: %w", err)
	}

	// Get the agent command
	agentCmd := s.getAgentCommand(agentType)
	cmd, err := tmux.BuildPaneCommand(workDir, agentCmd)
	if err != nil {
		_ = tmux.KillPane(paneID)
		return "", fmt.Errorf("building command: %w", err)
	}

	// Launch the agent
	if err := tmux.SendKeys(paneID, cmd, true); err != nil {
		_ = tmux.KillPane(paneID)
		return "", fmt.Errorf("launching agent: %w", err)
	}

	// Apply tiled layout (best-effort)
	if err := tmux.ApplyTiledLayout(session); err != nil {
		slog.Warn("failed to apply tiled layout after spawn", "session", session, "error", err)
	}

	return paneID, nil
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

func (s *DefaultPaneSpawner) getAgentCommand(agentType string) string {
	defaults := map[string]string{
		"claude":   "claude",
		"codex":    "codex",
		"gemini":   "gemini",
		"cursor":   "cursor",
		"windsurf": "windsurf",
		"aider":    "aider",
	}

	if s.config != nil {
		switch agentType {
		case "claude":
			if s.config.Agents.Claude != "" {
				return s.config.Agents.Claude
			}
		case "codex":
			if s.config.Agents.Codex != "" {
				return s.config.Agents.Codex
			}
		case "gemini":
			if s.config.Agents.Gemini != "" {
				return s.config.Agents.Gemini
			}
			// Future: add config support for other agents if needed
		}
	}

	if cmd, ok := defaults[agentType]; ok {
		return cmd
	}
	// Fall back to using the agent type name as the command.
	// This handles unknown/future agent types that match their CLI name.
	return agentType
}

func (tmuxPaneInputSender) SendKeys(paneID, text string, enter bool) error {
	return tmux.SendKeys(paneID, text, enter)
}

func (tmuxPaneInputSender) SendBuffer(paneID, text string, enter bool) error {
	return tmux.SendBuffer(paneID, text, enter)
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

// agentTypeShort returns the short form for pane naming.
func agentTypeShort(agentType string) string {
	switch strings.ToLower(agentType) {
	case "claude", "cc":
		return "cc"
	case "codex", "cod":
		return "cod"
	case "gemini", "gmi":
		return "gmi"
	case "cursor":
		return "cursor"
	case "windsurf":
		return "windsurf"
	case "aider":
		return "aider"
	default:
		return agentType
	}
}

// agentTypeLong returns the long form from short form.
func agentTypeLong(shortType string) string {
	switch strings.ToLower(shortType) {
	case "cc":
		return "claude"
	case "cod":
		return "codex"
	case "gmi":
		return "gemini"
	case "cursor":
		return "cursor"
	case "windsurf":
		return "windsurf"
	case "aider":
		return "aider"
	default:
		return shortType
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
	pending map[string]*PendingRotation
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
		monitor:   cfg.Monitor,
		compactor: cfg.Compactor,
		summary:   cfg.Summary,
		spawner:   cfg.Spawner,
		config:    cfg.Config,
		history:   make([]RotationEvent, 0),
		pending:   make(map[string]*PendingRotation),
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

	// Find agents above rotate threshold
	// Note: r.config.RotateThreshold is 0.0-1.0, but AgentsAboveThreshold expects 0-100 percentage
	agentsToRotate := r.monitor.AgentsAboveThreshold(r.config.RotateThreshold * 100)
	if len(agentsToRotate) == 0 {
		return nil, nil // No agents need rotation
	}

	var results []RotationResult

	// Process agents one at a time
	for _, agentInfo := range agentsToRotate {
		// Skip if already pending
		if _, exists := r.pending[agentInfo.AgentID]; exists {
			continue
		}

		// If confirmation is required, create a pending rotation instead
		if r.config.RequireConfirm {
			usagePercent := 0.0
			if agentInfo.Estimate != nil {
				usagePercent = agentInfo.Estimate.UsagePercent
			}
			pending := r.createPendingRotation(sessionName, agentInfo.AgentID, usagePercent, workDir)
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

// createPendingRotation creates a pending rotation entry for an agent.
func (r *Rotator) createPendingRotation(session, agentID string, contextPercent float64, workDir string) *PendingRotation {
	now := time.Now()
	timeoutSec := r.config.ConfirmTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 60 // Default to 60 seconds if not configured
	}

	defaultAction := ConfirmAction(r.config.DefaultConfirmAction)
	if defaultAction == "" {
		defaultAction = ConfirmRotate
	}

	// Find the pane ID for this agent
	paneID := ""
	if panes, err := r.spawner.GetPanes(session); err == nil {
		for _, p := range panes {
			if p.Title == agentID {
				paneID = p.ID
				break
			}
		}
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

	r.pending[agentID] = pending

	// Also persist to the pending rotation store for CLI access
	_ = AddPendingRotation(pending)

	return pending
}

// processExpiredPending handles pending rotations that have timed out.
func (r *Rotator) processExpiredPending(session, workDir string) {
	for agentID, pending := range r.pending {
		if !pending.IsExpired() {
			continue
		}

		// Execute the default action
		switch pending.DefaultAction {
		case ConfirmRotate:
			r.rotateAgent(session, agentID, workDir)
		case ConfirmCompact:
			if paneID := pending.PaneID; paneID != "" {
				r.tryCompaction(agentID, paneID)
			}
		case ConfirmIgnore:
			// Do nothing, just remove from pending
		case ConfirmPostpone:
			// Re-add with new timeout
			pending.TimeoutAt = time.Now().Add(30 * time.Minute)
			_ = AddPendingRotation(pending) // Update persistent store
			continue                        // Don't remove from pending
		}

		delete(r.pending, agentID)
		_ = RemovePendingRotation(agentID) // Also remove from persistent store
	}
}

// rotateAgent performs the full rotation flow for a single agent.
func (r *Rotator) rotateAgent(session, agentID, workDir string) RotationResult {
	startTime := time.Now()
	result := RotationResult{
		OldAgentID: agentID,
		Method:     RotationThresholdExceeded,
		State:      RotationStateInProgress,
		Timestamp:  startTime,
	}

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

	// Find the pane for this agent
	panes, err := r.spawner.GetPanes(session)
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

	var oldPane *tmux.Pane
	for i := range panes {
		// Use exact match only to avoid matching cc_1 against cc_10
		if panes[i].Title == agentID {
			oldPane = &panes[i]
			break
		}
	}

	if oldPane == nil {
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
	result.OldPaneID = oldPane.ID

	// Try compaction first if configured
	if r.config.TryCompactFirst && r.compactor != nil {
		compactResult := r.tryCompaction(agentID, oldPane.ID)
		if compactResult != nil && compactResult.Success {
			// Check if we're now below threshold
			estimate := r.monitor.GetEstimate(agentID)
			if estimate != nil && estimate.UsagePercent < r.config.RotateThreshold*100 {
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

	// Request handoff summary from the old agent
	summaryPrompt := r.summary.GeneratePrompt()
	if err := sendRotationPrompt(r.spawner, oldPane.ID, summaryPrompt); err != nil {
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

	// Wait for agent to respond (with timeout)
	time.Sleep(5 * time.Second) // Give agent time to start responding

	// Capture the summary response
	summaryText, err := tmux.CapturePaneOutput(oldPane.ID, 100)
	if err != nil {
		// Proceed with fallback summary
		summaryText = ""
	}

	// Parse the summary
	var handoffSummary *HandoffSummary
	if summaryText != "" {
		handoffSummary = r.summary.ParseAgentResponse(
			agentID,
			agentTypeLong(string(oldPane.Type)),
			session,
			summaryText,
		)
	} else {
		// Generate fallback summary from recent output
		recentOutput, _ := tmux.CapturePaneOutput(oldPane.ID, 50)
		handoffSummary = r.summary.GenerateFallbackSummary(
			agentID,
			agentTypeLong(string(oldPane.Type)),
			session,
			[]string{recentOutput},
		)
	}
	result.SummaryTokens = handoffSummary.TokenEstimate

	// Spawn replacement agent with same type
	agentType := agentTypeLong(string(oldPane.Type))
	newIndex := extractAgentIndex(agentID)
	newPaneID, err := r.spawner.SpawnAgent(session, agentType, newIndex, oldPane.Variant, workDir)
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
	result.NewPaneID = newPaneID
	result.NewAgentID = tmux.FormatPaneName(session, agentTypeShort(agentType), newIndex, oldPane.Variant)

	// Wait for new agent to be ready
	time.Sleep(3 * time.Second)

	// Send handoff context to new agent
	handoffContext := handoffSummary.FormatForNewAgent()
	if err := sendRotationPrompt(r.spawner, newPaneID, handoffContext); err != nil {
		// Non-fatal: agent is spawned but may not have context
		result.Error = fmt.Sprintf("warning: failed to send handoff context: %v", err)
	}

	// Kill the old pane
	if err := r.spawner.KillPane(oldPane.ID); err != nil {
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
	r.history = append(r.history, event)

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
	_ = RecordRotation(historyRecord)
}

// tryCompaction attempts to compact the agent's context.
func (r *Rotator) tryCompaction(agentID, paneID string) *CompactionResult {
	if r.compactor == nil {
		return nil
	}

	// Start compaction state
	state, err := r.compactor.NewCompactionState(agentID)
	if err != nil {
		return &CompactionResult{Success: false, Error: err.Error()}
	}

	// Derive agent type from the agent ID (format: session__type_index)
	agentType := deriveAgentTypeFromID(agentID)

	cmds := r.compactor.GetCompactionCommands(agentType)
	if len(cmds) == 0 {
		return &CompactionResult{Success: false, Error: "no compaction commands available"}
	}

	// Try the first compaction command (builtin if available)
	cmd := cmds[0]
	// Both slash commands and prompts need enter=true to be submitted
	if err := sendCompactionCommandToPane(tmuxPaneInputSender{}, paneID, cmd); err != nil {
		return &CompactionResult{Success: false, Error: fmt.Sprintf("failed to send compaction command: %v", err)}
	}

	// Set method based on whether this is a builtin command or summarization prompt
	method := CompactionBuiltin
	if cmd.IsPrompt {
		method = CompactionSummarize
	}
	state.UpdateState(cmd, method)

	// Wait for compaction to complete
	time.Sleep(cmd.WaitTime)

	// Finish and evaluate
	result, err := r.compactor.FinishCompaction(state)
	if err != nil {
		slog.Warn("compaction finish failed", "error", err)
	}
	return result
}

// extractAgentIndex extracts the numeric index from an agent ID.
// e.g., "myproject__cc_2" -> 2
func extractAgentIndex(agentID string) int {
	parts := strings.Split(agentID, "_")
	if len(parts) < 2 {
		return 1
	}
	// Find the last numeric part
	for i := len(parts) - 1; i >= 0; i-- {
		var n int
		if _, err := fmt.Sscanf(parts[i], "%d", &n); err == nil {
			return n
		}
	}
	return 1
}

// deriveAgentTypeFromID extracts agent type from agent ID.
// e.g., "myproject__cc_2" -> "claude", "myproject__cod_1" -> "codex"
func deriveAgentTypeFromID(agentID string) string {
	// Format: session__type_index
	parts := strings.Split(agentID, "__")
	if len(parts) < 2 {
		return "unknown"
	}
	typePart := parts[1]
	// typePart is like "cc_2" or "cod_1_variant"
	typeParts := strings.Split(typePart, "_")
	// strings.Split always returns at least one element, so typeParts[0] is safe
	return agentTypeLong(typeParts[0])
}

// GetHistory returns the rotation history.
func (r *Rotator) GetHistory() []RotationEvent {
	return r.history
}

// ClearHistory clears the rotation history.
func (r *Rotator) ClearHistory() {
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

	result := r.rotateAgent(session, agentID, workDir)
	result.Method = RotationManual
	return result
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
	result := make([]*PendingRotation, 0, len(r.pending))
	for _, p := range r.pending {
		result = append(result, p)
	}
	return result
}

// GetPendingRotation returns a specific pending rotation by agent ID.
func (r *Rotator) GetPendingRotation(agentID string) *PendingRotation {
	return r.pending[agentID]
}

// HasPendingRotation returns true if there is a pending rotation for the agent.
func (r *Rotator) HasPendingRotation(agentID string) bool {
	_, exists := r.pending[agentID]
	return exists
}

// ConfirmRotation handles user confirmation of a pending rotation.
// Returns the result of the action taken.
func (r *Rotator) ConfirmRotation(agentID string, action ConfirmAction, postponeMinutes int) RotationResult {
	pending := r.pending[agentID]
	if pending == nil {
		return RotationResult{
			OldAgentID: agentID,
			State:      RotationStateFailed,
			Error:      "no pending rotation found for agent",
			Timestamp:  time.Now(),
		}
	}

	result := RotationResult{
		OldAgentID: agentID,
		Timestamp:  time.Now(),
	}

	switch action {
	case ConfirmRotate:
		// Remove from pending and perform the rotation
		delete(r.pending, agentID)
		_ = RemovePendingRotation(agentID) // Also remove from persistent store
		return r.rotateAgent(pending.SessionName, agentID, pending.WorkDir)

	case ConfirmCompact:
		// Try compaction first
		if pending.PaneID == "" {
			result.State = RotationStateFailed
			result.Error = "cannot compact: pane ID unknown"
			return result
		}
		delete(r.pending, agentID)
		_ = RemovePendingRotation(agentID) // Also remove from persistent store
		compactResult := r.tryCompaction(agentID, pending.PaneID)
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
		// Cancel the rotation
		delete(r.pending, agentID)
		_ = RemovePendingRotation(agentID) // Also remove from persistent store
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
		pending.TimeoutAt = time.Now().Add(time.Duration(minutes) * time.Minute)
		// Update persistent store with new timeout
		_ = AddPendingRotation(pending)
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
	if _, exists := r.pending[agentID]; exists {
		delete(r.pending, agentID)
		_ = RemovePendingRotation(agentID) // Also remove from persistent store
		return true
	}
	return false
}

// ClearPending removes all pending rotations.
func (r *Rotator) ClearPending() {
	r.pending = make(map[string]*PendingRotation)
	_ = DefaultPendingRotationStore.Clear() // Also clear persistent store
}
