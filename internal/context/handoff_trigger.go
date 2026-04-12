// Package context provides context window monitoring for AI agent orchestration.
// handoff_trigger.go implements proactive handoff generation before context compaction.
//
// This implements the "Compound, Don't Compact" philosophy: instead of losing
// information through compaction, we generate handoffs to preserve context
// that can be passed to a new session.
package context

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/handoff"
)

// HandoffTriggerConfig configures the proactive handoff trigger.
type HandoffTriggerConfig struct {
	// PollInterval is how often to check agents for handoff triggers
	PollInterval time.Duration
	// WarnThreshold is the usage percentage for warning callbacks (default 70%)
	WarnThreshold float64
	// TriggerThreshold is the usage percentage for handoff generation (default 75%)
	TriggerThreshold float64
	// Cooldown is minimum time between handoffs per agent
	Cooldown time.Duration
	// ProjectDir is the project directory for handoff generation
	ProjectDir string
}

// DefaultHandoffTriggerConfig returns sensible defaults.
func DefaultHandoffTriggerConfig() HandoffTriggerConfig {
	return HandoffTriggerConfig{
		PollInterval:     30 * time.Second,
		WarnThreshold:    70.0,
		TriggerThreshold: 75.0,
		Cooldown:         5 * time.Minute,
	}
}

// HandoffTriggerEvent represents a handoff trigger event.
type HandoffTriggerEvent struct {
	AgentID      string    `json:"agent_id"`
	PaneID       string    `json:"pane_id"`
	AgentType    string    `json:"agent_type"`
	SessionName  string    `json:"session_name"`
	UsagePercent float64   `json:"usage_percent"`
	Reason       string    `json:"reason"`
	HandoffPath  string    `json:"handoff_path,omitempty"`
	TriggeredAt  time.Time `json:"triggered_at"`
	Error        string    `json:"error,omitempty"`
}

// HandoffTrigger monitors agents and generates handoffs proactively before context exhaustion.
type HandoffTrigger struct {
	config    HandoffTriggerConfig
	monitor   *ContextMonitor
	predictor *ContextPredictor
	generator *handoff.Generator
	writer    *handoff.Writer

	// Tracking state
	mu           sync.RWMutex
	lifecycleMu  sync.Mutex
	lastHandoff  map[string]time.Time // agentID -> last handoff time
	lastWarning  map[string]time.Time // agentID -> last warning time
	activeAgents map[string]bool      // agentID -> currently generating handoff

	// Event handlers
	onWarning   func(HandoffTriggerEvent)
	onTriggered func(HandoffTriggerEvent)

	// Control
	stopCh  chan struct{}
	doneCh  chan struct{}
	running bool

	logger *slog.Logger
}

// NewHandoffTrigger creates a new proactive handoff trigger.
func NewHandoffTrigger(
	config HandoffTriggerConfig,
	monitor *ContextMonitor,
	predictor *ContextPredictor,
) *HandoffTrigger {
	if config.PollInterval <= 0 {
		config.PollInterval = 30 * time.Second
	}
	if config.WarnThreshold <= 0 {
		config.WarnThreshold = 70.0
	}
	if config.TriggerThreshold <= 0 {
		config.TriggerThreshold = 75.0
	}
	if config.Cooldown <= 0 {
		config.Cooldown = 5 * time.Minute
	}

	return &HandoffTrigger{
		config:       config,
		monitor:      monitor,
		predictor:    predictor,
		generator:    handoff.NewGenerator(config.ProjectDir),
		writer:       handoff.NewWriter(config.ProjectDir),
		lastHandoff:  make(map[string]time.Time),
		lastWarning:  make(map[string]time.Time),
		activeAgents: make(map[string]bool),
		logger:       slog.Default().With("component", "context.handoff_trigger"),
	}
}

// SetWarningHandler sets the callback for warning events (70% threshold).
func (t *HandoffTrigger) SetWarningHandler(handler func(HandoffTriggerEvent)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onWarning = handler
}

// SetTriggeredHandler sets the callback for handoff trigger events.
func (t *HandoffTrigger) SetTriggeredHandler(handler func(HandoffTriggerEvent)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onTriggered = handler
}

// Start begins the background monitoring loop.
// It is a no-op while the trigger is already running.
func (t *HandoffTrigger) Start() {
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

	t.logger.Info("starting handoff trigger",
		"poll_interval", t.config.PollInterval,
		"warn_threshold", t.config.WarnThreshold,
		"trigger_threshold", t.config.TriggerThreshold,
	)

	go t.monitorLoop(stopCh, doneCh)
}

// Stop halts the background monitoring loop. Safe to call multiple times.
func (t *HandoffTrigger) Stop() {
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

	t.logger.Info("stopping handoff trigger")
	close(stopCh)
	<-doneCh
}

// monitorLoop runs the periodic check for handoff triggers.
func (t *HandoffTrigger) monitorLoop(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)

	ticker := time.NewTicker(t.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			paths, err := t.Check()
			if err != nil {
				t.logger.Error("periodic check failed", "error", err)
			} else if len(paths) > 0 {
				t.logger.Info("periodic check generated handoffs", "count", len(paths))
			}
		}
	}
}

// Check evaluates all agents and triggers handoffs as needed.
// Returns list of generated handoff paths.
func (t *HandoffTrigger) Check() ([]string, error) {
	t.logger.Debug("checking all agents for handoff triggers")

	var paths []string

	estimates := t.monitor.GetAllEstimates()
	for agentID := range estimates {
		path, err := t.checkAgent(agentID)
		if err != nil {
			t.logger.Error("failed to check agent",
				"agent_id", agentID,
				"error", err,
			)
			continue
		}
		if path != "" {
			paths = append(paths, path)
		}
	}

	return paths, nil
}

// checkAgent checks a single agent and triggers handoff if needed.
// Returns the handoff path if one was generated.
func (t *HandoffTrigger) checkAgent(agentID string) (string, error) {
	// Skip if already generating handoff for this agent
	t.mu.RLock()
	if t.activeAgents[agentID] {
		t.mu.RUnlock()
		return "", nil
	}
	t.mu.RUnlock()

	// Get handoff recommendation
	rec := t.monitor.ShouldTriggerHandoff(agentID, t.predictor)
	if rec == nil {
		return "", nil
	}

	state := t.monitor.GetState(agentID)
	if state == nil {
		return "", nil
	}

	event := HandoffTriggerEvent{
		AgentID:      agentID,
		PaneID:       state.PaneID,
		AgentType:    state.AgentType,
		SessionName:  state.SessionName,
		UsagePercent: rec.UsagePercent,
		Reason:       rec.Reason,
		TriggeredAt:  time.Now(),
	}

	// Check for warning (70%)
	if rec.ShouldWarn {
		t.handleWarning(event)
	}

	// Check if handoff should be triggered
	if !rec.ShouldTrigger {
		return "", nil
	}

	// Check cooldown
	if !t.canTrigger(agentID) {
		t.mu.RLock()
		lastHandoff := t.lastHandoff[agentID]
		t.mu.RUnlock()
		t.logger.Debug("skipping handoff due to cooldown",
			"agent_id", agentID,
			"last_handoff", lastHandoff,
		)
		return "", nil
	}

	// Generate handoff
	path, err := t.TriggerForAgent(agentID)
	if err != nil {
		event.Error = err.Error()
		t.handleTriggered(event)
		return "", err
	}

	event.HandoffPath = path
	t.handleTriggered(event)

	return path, nil
}

// canTrigger checks if a handoff is allowed for an agent (cooldown check).
func (t *HandoffTrigger) canTrigger(agentID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	lastTime, ok := t.lastHandoff[agentID]
	if !ok {
		return true
	}

	return time.Since(lastTime) >= t.config.Cooldown
}

// handleWarning invokes the warning callback with debouncing.
func (t *HandoffTrigger) handleWarning(event HandoffTriggerEvent) {
	t.mu.Lock()
	lastTime := t.lastWarning[event.AgentID]
	// Debounce warnings to every 2 minutes
	if time.Since(lastTime) < 2*time.Minute {
		t.mu.Unlock()
		return
	}
	t.lastWarning[event.AgentID] = time.Now()
	handler := t.onWarning
	t.mu.Unlock()

	t.logger.Warn("context warning threshold exceeded",
		"agent_id", event.AgentID,
		"usage_percent", event.UsagePercent,
		"reason", event.Reason,
	)

	if handler != nil {
		handler(event)
	}
}

// handleTriggered invokes the triggered callback.
func (t *HandoffTrigger) handleTriggered(event HandoffTriggerEvent) {
	t.mu.RLock()
	handler := t.onTriggered
	t.mu.RUnlock()

	if event.Error != "" {
		t.logger.Error("handoff trigger failed",
			"agent_id", event.AgentID,
			"error", event.Error,
		)
	} else {
		t.logger.Info("handoff triggered",
			"agent_id", event.AgentID,
			"pane_id", event.PaneID,
			"usage_percent", event.UsagePercent,
			"reason", event.Reason,
			"path", event.HandoffPath,
		)
	}

	if handler != nil {
		handler(event)
	}
}

// TriggerForAgent forces handoff generation for a specific agent.
// This can be called manually to force a handoff regardless of cooldown.
func (t *HandoffTrigger) TriggerForAgent(agentID string) (string, error) {
	state := t.monitor.GetState(agentID)
	if state == nil {
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	// Mark as active
	t.mu.Lock()
	t.activeAgents[agentID] = true
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.activeAgents, agentID)
		t.lastHandoff[agentID] = time.Now()
		t.mu.Unlock()
	}()

	t.logger.Debug("generating handoff for agent",
		"agent_id", agentID,
		"session", state.SessionName,
		"pane_id", state.PaneID,
	)

	// Get current estimate for token info
	estimate := t.monitor.GetEstimate(agentID)
	var tokensUsed, tokensMax int
	if estimate != nil {
		tokensUsed = int(estimate.TokensUsed)
		tokensMax = int(estimate.ContextLimit)
	}

	// Generate handoff using the generator
	ctx := context.Background()
	h, err := t.generator.GenerateHandoff(ctx, handoff.GenerateHandoffOptions{
		SessionName: state.SessionName,
		AgentType:   state.AgentType,
		PaneID:      state.PaneID,
		TokensUsed:  tokensUsed,
		TokensMax:   tokensMax,
	})
	if err != nil {
		return "", fmt.Errorf("generate handoff: %w", err)
	}

	// If transcript path available, try to enrich from it
	if state.TranscriptPath != "" {
		transcriptH, err := t.generator.GenerateFromTranscript(state.SessionName, state.TranscriptPath)
		if err == nil && transcriptH != nil {
			// Merge transcript findings into main handoff
			if h.Goal == "" && transcriptH.Goal != "" {
				h.Goal = transcriptH.Goal
			}
			if h.Now == "" && transcriptH.Now != "" {
				h.Now = transcriptH.Now
			}
			// Merge file changes
			h.Files.Modified = append(h.Files.Modified, transcriptH.Files.Modified...)
			h.Files.Created = append(h.Files.Created, transcriptH.Files.Created...)
		}
	}

	// Write auto-handoff
	path, err := t.writer.WriteAuto(h)
	if err != nil {
		return "", fmt.Errorf("write handoff: %w", err)
	}

	t.logger.Info("auto-handoff generated",
		"agent_id", agentID,
		"session", state.SessionName,
		"path", path,
		"tokens_pct", h.TokensPct,
	)

	return path, nil
}

// GetStatus returns the current status for all tracked agents.
func (t *HandoffTrigger) GetStatus() map[string]HandoffStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	status := make(map[string]HandoffStatus)
	for agentID, lastTime := range t.lastHandoff {
		status[agentID] = HandoffStatus{
			LastHandoff:   lastTime,
			IsGenerating:  t.activeAgents[agentID],
			CooldownUntil: lastTime.Add(t.config.Cooldown),
		}
	}

	return status
}

// HandoffStatus represents the handoff status for an agent.
type HandoffStatus struct {
	LastHandoff   time.Time `json:"last_handoff"`
	IsGenerating  bool      `json:"is_generating"`
	CooldownUntil time.Time `json:"cooldown_until"`
}
