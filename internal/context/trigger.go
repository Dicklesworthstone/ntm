// Package context provides context window monitoring for AI agent orchestration.
// trigger.go implements proactive compaction triggering based on prediction thresholds.
package context

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// CompactionTriggerConfig configures the proactive compaction trigger.
type CompactionTriggerConfig struct {
	// PollInterval is how often to check predictions for compaction triggers
	PollInterval time.Duration
	// AutoCompact enables automatic compaction triggering
	AutoCompact bool
	// CompactionCooldown is minimum time between compaction attempts per agent
	CompactionCooldown time.Duration
	// WaitAfterCommand is how long to wait for compaction to complete
	WaitAfterCommand time.Duration
	// EnableRecoveryInjection controls whether to inject recovery context post-compaction
	EnableRecoveryInjection bool
}

// DefaultCompactionTriggerConfig returns sensible defaults.
func DefaultCompactionTriggerConfig() CompactionTriggerConfig {
	return CompactionTriggerConfig{
		PollInterval:            30 * time.Second,
		AutoCompact:             true,
		CompactionCooldown:      5 * time.Minute,
		WaitAfterCommand:        15 * time.Second,
		EnableRecoveryInjection: true,
	}
}

// CompactionTriggerEvent represents a compaction trigger event.
type CompactionTriggerEvent struct {
	AgentID          string            `json:"agent_id"`
	PaneID           string            `json:"pane_id"`
	AgentType        tmux.AgentType    `json:"agent_type"`
	Prediction       *Prediction       `json:"prediction"`
	TriggeredAt      time.Time         `json:"triggered_at"`
	CompactionResult *CompactionResult `json:"compaction_result,omitempty"`
}

// CompactionTrigger monitors agents and triggers compaction proactively.
type CompactionTrigger struct {
	config    CompactionTriggerConfig
	monitor   *ContextMonitor
	compactor *Compactor
	predictor *ContextPredictor

	// Tracking state
	mu                sync.RWMutex
	lifecycleMu       sync.Mutex
	lastCompaction    map[string]time.Time // agentID -> last compaction time
	activeCompactions map[string]bool      // agentID -> currently compacting

	// Event handlers
	onCompactionTriggered func(CompactionTriggerEvent)
	onCompactionComplete  func(CompactionTriggerEvent)

	// Control
	stopCh  chan struct{}
	doneCh  chan struct{}
	running bool
}

// NewCompactionTrigger creates a new proactive compaction trigger.
func NewCompactionTrigger(
	config CompactionTriggerConfig,
	monitor *ContextMonitor,
	compactor *Compactor,
	predictor *ContextPredictor,
) *CompactionTrigger {
	return &CompactionTrigger{
		config:            config,
		monitor:           monitor,
		compactor:         compactor,
		predictor:         predictor,
		lastCompaction:    make(map[string]time.Time),
		activeCompactions: make(map[string]bool),
	}
}

// SetCompactionTriggeredHandler sets the handler called when compaction is triggered.
func (t *CompactionTrigger) SetCompactionTriggeredHandler(handler func(CompactionTriggerEvent)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onCompactionTriggered = handler
}

// SetCompactionCompleteHandler sets the handler called when compaction completes.
func (t *CompactionTrigger) SetCompactionCompleteHandler(handler func(CompactionTriggerEvent)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onCompactionComplete = handler
}

// Start begins the background monitoring loop.
// It is a no-op while the trigger is already running.
func (t *CompactionTrigger) Start() {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()

	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	t.stopCh = stopCh
	t.doneCh = doneCh
	t.running = true
	t.mu.Unlock()

	go t.monitorLoop(stopCh, doneCh)
}

// Stop halts the background monitoring loop. Safe to call multiple times.
func (t *CompactionTrigger) Stop() {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()

	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return
	}
	stopCh := t.stopCh
	doneCh := t.doneCh
	t.stopCh = nil
	t.doneCh = nil
	t.running = false
	t.mu.Unlock()

	close(stopCh)
	<-doneCh
}

// monitorLoop runs the periodic check for compaction triggers.
func (t *CompactionTrigger) monitorLoop(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)

	// ubs:ignore - ticker stopped via defer below
	ticker := time.NewTicker(t.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			t.checkAllAgents()
		}
	}
}

// checkAllAgents checks all monitored agents for compaction triggers.
func (t *CompactionTrigger) checkAllAgents() {
	if !t.config.AutoCompact {
		return
	}

	estimates := t.monitor.GetAllEstimates()
	for agentID := range estimates {
		t.checkAgent(agentID)
	}
}

// checkAgent checks a single agent for compaction trigger.
func (t *CompactionTrigger) checkAgent(agentID string) {
	// Skip if already compacting
	t.mu.RLock()
	if t.activeCompactions[agentID] {
		t.mu.RUnlock()
		return
	}
	t.mu.RUnlock()

	// Check cooldown
	if !t.canCompact(agentID) {
		return
	}

	// Get agent state for pane info
	state := t.monitor.GetState(agentID)
	if state == nil {
		return
	}

	// Get context limit for model
	contextLimit := GetContextLimit(state.Model)

	// Get prediction
	prediction := t.predictor.PredictExhaustion(contextLimit)
	if prediction == nil {
		return
	}

	// Check if compaction is needed
	if !prediction.ShouldCompact {
		return
	}

	// Trigger compaction
	t.triggerCompaction(agentID, state, prediction)
}

// canCompact checks if compaction is allowed for an agent (cooldown check).
func (t *CompactionTrigger) canCompact(agentID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	lastTime, ok := t.lastCompaction[agentID]
	if !ok {
		return true
	}

	return time.Since(lastTime) >= t.config.CompactionCooldown
}

// triggerCompaction initiates compaction for an agent.
func (t *CompactionTrigger) triggerCompaction(agentID string, state *ContextState, prediction *Prediction) {
	// Mark as compacting
	t.mu.Lock()
	t.activeCompactions[agentID] = true
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.activeCompactions, agentID)
		t.lastCompaction[agentID] = time.Now()
		t.mu.Unlock()
	}()

	// Determine agent type from pane
	var agentType tmux.AgentType
	if state.AgentType != "" {
		agentType = tmux.AgentType(state.AgentType)
	} else {
		agentType = t.detectAgentType(state.PaneID)
		if agentType != tmux.AgentUser {
			t.monitor.SetAgentType(agentID, string(agentType))
		}
	}

	event := CompactionTriggerEvent{
		AgentID:     agentID,
		PaneID:      state.PaneID,
		AgentType:   agentType,
		Prediction:  prediction,
		TriggeredAt: time.Now(),
	}

	slog.Info("Proactive compaction triggered",
		"agent_id", agentID,
		"pane_id", state.PaneID,
		"agent_type", agentType,
		"usage_percent", prediction.CurrentUsage*100,
		"minutes_to_exhaustion", prediction.MinutesToExhaustion,
	)

	// Notify handler
	t.mu.RLock()
	handler := t.onCompactionTriggered
	t.mu.RUnlock()
	if handler != nil {
		handler(event)
	}

	// Emit compaction triggered alert
	alerts.EmitCompactionTriggered(alerts.CompactionAlertData{
		AgentID:        agentID,
		Session:        "", // Session determined by pane
		Pane:           state.PaneID,
		ContextUsage:   prediction.CurrentUsage * 100,
		MinutesToLimit: prediction.MinutesToExhaustion,
	})

	// Execute compaction
	result := t.executeCompaction(agentID, state.PaneID, agentType)
	event.CompactionResult = result

	// Inject recovery context if enabled and compaction succeeded
	if t.config.EnableRecoveryInjection && result != nil && result.Success {
		t.injectRecoveryContext(state.PaneID, agentType)
	}

	// Emit completion alert
	if result != nil {
		alertData := alerts.CompactionAlertData{
			AgentID:      agentID,
			Pane:         state.PaneID,
			ContextUsage: prediction.CurrentUsage * 100,
			Method:       string(result.Method),
			TokensBefore: result.TokensBefore,
			TokensAfter:  result.TokensAfter,
			UsageBefore:  result.UsageBefore,
			UsageAfter:   result.UsageAfter,
			DurationMs:   result.Duration.Milliseconds(),
			Error:        result.Error,
		}
		if result.Success {
			alerts.EmitCompactionComplete(alertData)
		} else {
			alerts.EmitCompactionFailed(alertData)
		}
	}

	// Notify completion
	t.mu.RLock()
	completeHandler := t.onCompactionComplete
	t.mu.RUnlock()
	if completeHandler != nil {
		completeHandler(event)
	}
}

// executeCompaction sends the compaction command to an agent.
func (t *CompactionTrigger) executeCompaction(agentID, paneID string, agentType tmux.AgentType) *CompactionResult {
	// Create compaction state
	compState, err := t.compactor.NewCompactionState(agentID)
	if err != nil {
		slog.Error("Failed to create compaction state", "agent_id", agentID, "error", err)
		return &CompactionResult{
			Success: false,
			Method:  CompactionFailed,
			Error:   err.Error(),
		}
	}

	// Get compaction commands for this agent type
	commands := t.compactor.GetCompactionCommands(string(agentType))
	if len(commands) == 0 {
		slog.Warn("No compaction commands available for agent type", "agent_type", agentType)
		return &CompactionResult{
			Success: false,
			Method:  CompactionFailed,
			Error:   "no compaction commands available",
		}
	}

	// Try each compaction command in sequence
	for _, cmd := range commands {
		slog.Info("Sending compaction command",
			"agent_id", agentID,
			"pane_id", paneID,
			"command_type", cmd.Description,
			"is_prompt", cmd.IsPrompt,
		)

		// Send the command.
		err := t.sendCompactionCommand(paneID, cmd)
		if err != nil {
			slog.Error("Failed to send compaction command",
				"pane_id", paneID,
				"error", err,
			)
			continue
		}

		compState.UpdateState(cmd, compactionMethodForCommand(cmd))

		// Wait for compaction to complete
		time.Sleep(cmd.WaitTime)

		// Check result
		result, err := t.compactor.FinishCompaction(compState)
		if err != nil {
			slog.Warn("Failed to evaluate compaction", "error", err)
			continue
		}

		if result.Success {
			slog.Info("Compaction succeeded",
				"agent_id", agentID,
				"usage_before", result.UsageBefore,
				"usage_after", result.UsageAfter,
				"tokens_reclaimed", result.TokensReclaimed,
			)
			return result
		}

		slog.Info("Compaction method did not achieve target reduction, trying next",
			"method", result.Method,
			"error", result.Error,
		)
	}

	return &CompactionResult{
		Success: false,
		Method:  CompactionFailed,
		Error:   "all compaction methods exhausted",
	}
}

// sendCompactionCommand sends a compaction command to a pane.
func (t *CompactionTrigger) sendCompactionCommand(paneID string, cmd CompactionCommand) error {
	return sendCompactionCommandToPane(tmuxPaneInputSender{}, paneID, cmd)
}

// injectRecoveryContext injects recovery context after successful compaction.
func (t *CompactionTrigger) injectRecoveryContext(paneID string, agentType tmux.AgentType) {
	// Generate recovery context message
	recoveryPrompt := t.generateRecoveryPrompt(agentType)
	if recoveryPrompt == "" {
		return
	}

	// Wait a moment for compaction to settle
	time.Sleep(2 * time.Second)

	// Send recovery context
	err := tmux.SendKeys(paneID, recoveryPrompt, true)
	if err != nil {
		slog.Error("Failed to inject recovery context",
			"pane_id", paneID,
			"error", err,
		)
		return
	}

	slog.Info("Recovery context injected", "pane_id", paneID)
}

// generateRecoveryPrompt generates the recovery context prompt for post-compaction.
func (t *CompactionTrigger) generateRecoveryPrompt(agentType tmux.AgentType) string {
	// Different prompts per agent type
	switch agentType {
	case tmux.AgentClaude:
		return "Reread AGENTS.md so it's still fresh in your mind. Continue working on your assigned task."
	case tmux.AgentCodex:
		return "Reread AGENTS.md so it's still fresh in your mind. Continue working on your assigned task."
	case tmux.AgentGemini:
		return "Reread AGENTS.md so it's still fresh in your mind. Continue working on your assigned task."
	case tmux.AgentCursor:
		return "Reread AGENTS.md so it's still fresh in your mind. Continue working on your assigned task."
	case tmux.AgentWindsurf:
		return "Reread AGENTS.md so it's still fresh in your mind. Continue working on your assigned task."
	case tmux.AgentAider:
		return "Reread AGENTS.md so it's still fresh in your mind. Continue working on your assigned task."
	default:
		return "Continue with your previous task."
	}
}

// detectAgentType detects the agent type from a pane ID or title.
func (t *CompactionTrigger) detectAgentType(paneID string) tmux.AgentType {
	// Try to get pane info from tmux by checking all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		return tmux.AgentUser
	}

	for _, session := range sessions {
		panes, err := tmux.GetPanes(session.Name)
		if err != nil {
			continue
		}
		for _, pane := range panes {
			if pane.ID == paneID {
				return pane.Type
			}
		}
	}

	return tmux.AgentUser
}

// TriggerCompactionNow forces an immediate compaction check for a specific agent.
// This can be called manually to force compaction regardless of cooldown.
func (t *CompactionTrigger) TriggerCompactionNow(agentID string) (*CompactionResult, error) {
	state := t.monitor.GetState(agentID)
	if state == nil {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}

	// Get context limit
	contextLimit := GetContextLimit(state.Model)

	// Get prediction (even if not at threshold, we'll force it)
	prediction := t.predictor.PredictExhaustion(contextLimit)
	if prediction == nil {
		// Create a synthetic prediction for forced compaction
		prediction = &Prediction{
			CurrentUsage:  0,
			ShouldCompact: true,
		}
	}

	// Force the prediction to trigger
	prediction.ShouldCompact = true

	// Detect agent type
	agentType := t.detectAgentType(state.PaneID)

	// Execute compaction directly
	return t.executeCompaction(agentID, state.PaneID, agentType), nil
}

// GetCompactionStatus returns the current compaction status for all agents.
func (t *CompactionTrigger) GetCompactionStatus() map[string]CompactionStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	status := make(map[string]CompactionStatus)
	for agentID, lastTime := range t.lastCompaction {
		status[agentID] = CompactionStatus{
			LastCompaction: lastTime,
			IsCompacting:   t.activeCompactions[agentID],
			CooldownUntil:  lastTime.Add(t.config.CompactionCooldown),
		}
	}

	return status
}

// CompactionStatus represents the compaction status for an agent.
type CompactionStatus struct {
	LastCompaction time.Time `json:"last_compaction"`
	IsCompacting   bool      `json:"is_compacting"`
	CooldownUntil  time.Time `json:"cooldown_until"`
}
