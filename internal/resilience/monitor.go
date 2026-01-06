// Package resilience provides auto-restart and recovery functionality for agents.
package resilience

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/notify"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Overridable hooks for tests.
// Protected by hooksMu for concurrent access from spawned goroutines.
var (
	hooksMu          sync.RWMutex
	sendKeysFn       = tmux.SendKeys
	buildPaneCmdFn   = tmux.BuildPaneCommand
	sleepFn          = time.Sleep
	checkSessionFn   = health.CheckSession
	displayMessageFn = tmux.DisplayMessage
)

// AgentState tracks the state of an individual agent for restart purposes
type AgentState struct {
	PaneID            string
	PaneIndex         int
	AgentType         string // cc, cod, gmi
	Model             string // Model variant (opus, sonnet, etc.)
	Command           string // Original launch command
	RestartCount      int
	LastCrash         time.Time
	LastRestart       time.Time // When agent was last restarted
	Healthy           bool
	RateLimited       bool      // Currently rate limited
	LastRateLimitTime time.Time // When rate limit was last detected
	WaitSeconds       int       // Suggested wait time from rate limit message
}

// Monitor watches agent health and handles auto-restart
type Monitor struct {
	session    string
	projectDir string
	cfg        *config.Config
	notifier   *notify.Notifier

	autoRestart bool // Whether to automatically restart crashed agents

	mu     sync.RWMutex
	agents map[string]*AgentState // keyed by pane ID
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMonitor creates a new resilience monitor for a session
func NewMonitor(session, projectDir string, cfg *config.Config, autoRestart bool) *Monitor {
	var notifier *notify.Notifier
	if cfg.Notifications.Enabled {
		notifier = notify.New(cfg.Notifications)
	}

	return &Monitor{
		session:     session,
		projectDir:  projectDir,
		cfg:         cfg,
		notifier:    notifier,
		autoRestart: autoRestart,
		agents:      make(map[string]*AgentState),
		done:        make(chan struct{}),
	}
}

// RegisterAgent adds an agent to be monitored
func (m *Monitor) RegisterAgent(paneID string, paneIndex int, agentType, model, command string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.agents[paneID] = &AgentState{
		PaneID:    paneID,
		PaneIndex: paneIndex,
		AgentType: agentType,
		Model:     model,
		Command:   command,
		Healthy:   true,
	}
}

// ScanAndRegisterAgents discovers agents from existing tmux panes
func (m *Monitor) ScanAndRegisterAgents() error {
	panes, err := tmux.GetPanes(m.session)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, p := range panes {
		// Only monitor agent panes (not user or unknown)
		if p.Type == tmux.AgentClaude || p.Type == tmux.AgentCodex || p.Type == tmux.AgentGemini {
			// Skip if already registered
			if _, exists := m.agents[p.ID]; exists {
				continue
			}

			// Reconstruct command template
			var agentCmdTemplate string
			switch p.Type {
			case tmux.AgentClaude:
				agentCmdTemplate = m.cfg.Agents.Claude
			case tmux.AgentCodex:
				agentCmdTemplate = m.cfg.Agents.Codex
			case tmux.AgentGemini:
				agentCmdTemplate = m.cfg.Agents.Gemini
			}

			// Resolve model
			modelName := m.cfg.Models.GetModelName(string(p.Type), p.Variant)

			// Generate command
			cmd, err := config.GenerateAgentCommand(agentCmdTemplate, config.AgentTemplateVars{
				Model:       modelName,
				ModelAlias:  p.Variant,
				SessionName: m.session,
				PaneIndex:   p.Index,
				AgentType:   string(p.Type),
				ProjectDir:  m.projectDir,
				// Note: SystemPromptFile is lost in reconstruction
			})

			if err != nil {
				log.Printf("[resilience] Failed to reconstruct command for %s: %v", p.ID, err)
				continue
			}

			m.agents[p.ID] = &AgentState{
				PaneID:    p.ID,
				PaneIndex: p.Index,
				AgentType: string(p.Type),
				Model:     p.Variant,
				Command:   cmd,
				Healthy:   true,
			}
		}
	}
	return nil
}

// Start begins monitoring agent health in the background
func (m *Monitor) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	go m.monitorLoop(ctx)
}

// Stop stops the monitor gracefully.
// Safe to call even if Start() was never called.
func (m *Monitor) Stop() {
	if m.cancel == nil {
		// Start() was never called, nothing to stop
		return
	}
	m.cancel()
	<-m.done
}

// GetRestartCount returns the number of restarts for an agent
func (m *Monitor) GetRestartCount(paneID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if agent, ok := m.agents[paneID]; ok {
		return agent.RestartCount
	}
	return 0
}

// GetAgentStates returns a copy of all agent states
func (m *Monitor) GetAgentStates() map[string]AgentState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make(map[string]AgentState, len(m.agents))
	for id, agent := range m.agents {
		states[id] = *agent
	}
	return states
}

// monitorLoop is the main health check loop
func (m *Monitor) monitorLoop(ctx context.Context) {
	defer close(m.done)

	checkInterval := time.Duration(m.cfg.Resilience.HealthCheckSeconds) * time.Second
	if checkInterval < time.Second {
		checkInterval = 10 * time.Second // Minimum 10 seconds
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkHealth()
		}
	}
}

// checkHealth performs a health check on all monitored agents
func (m *Monitor) checkHealth() {
	// Snapshot hook under lock for thread-safe access
	hooksMu.RLock()
	checkFn := checkSessionFn
	hooksMu.RUnlock()

	// Get health status for the session
	sessionHealth, err := checkFn(m.session)
	if err != nil {
		log.Printf("[resilience] health check failed: %v", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Build a map of pane health by pane ID
	healthByPaneID := make(map[string]*health.AgentHealth)
	for i := range sessionHealth.Agents {
		agent := &sessionHealth.Agents[i]
		healthByPaneID[agent.PaneID] = agent
	}

	// Check each monitored agent
	for paneID, agentState := range m.agents {
		agentHealth, exists := healthByPaneID[paneID]

		// If pane doesn't exist anymore, agent crashed hard
		if !exists {
			if agentState.Healthy {
				m.handleCrash(agentState, "Pane no longer exists")
			}
			continue
		}

		// Check for rate limit (separate from crash handling)
		if m.cfg.Resilience.RateLimit.Detect && agentHealth.RateLimited {
			// Only notify if this is a new rate limit event (not already rate limited)
			if !agentState.RateLimited {
				m.handleRateLimit(agentState, agentHealth.WaitSeconds)
			}
		} else if agentState.RateLimited {
			// Rate limit cleared
			agentState.RateLimited = false
			agentState.WaitSeconds = 0
			log.Printf("[resilience] Agent %s rate limit cleared", agentState.PaneID)
		}

		// Check for error status or process exit
		if agentHealth.Status == health.StatusError ||
			agentHealth.ProcessStatus == health.ProcessExited {

			if agentState.Healthy {
				// Ignore transient errors during startup grace period
				if !agentState.LastRestart.IsZero() && time.Since(agentState.LastRestart) < 5*time.Second {
					continue
				}

				reason := "Agent unhealthy"
				if len(agentHealth.Issues) > 0 {
					reason = agentHealth.Issues[0].Message
				}
				m.handleCrash(agentState, reason)
			}
		} else {
			// Agent is healthy again
			agentState.Healthy = true
		}
	}
}

// handleRateLimit processes a detected rate limit event
func (m *Monitor) handleRateLimit(agent *AgentState, waitSeconds int) {
	agent.RateLimited = true
	agent.LastRateLimitTime = time.Now()
	agent.WaitSeconds = waitSeconds

	log.Printf("[resilience] Agent %s (pane %d, type %s) hit rate limit (wait %ds)",
		agent.PaneID, agent.PaneIndex, agent.AgentType, waitSeconds)

	// Snapshot values for async operations
	session := m.session
	paneID := agent.PaneID
	paneIndex := agent.PaneIndex
	agentType := agent.AgentType
	notifyEnabled := m.cfg.Resilience.RateLimit.Notify
	rotateConfig := m.cfg.Rotation

	// Run notifications and rotation triggers asynchronously
	go func() {
		// Send rate limit notification if enabled
		if notifyEnabled && m.notifier != nil {
			event := notify.NewRateLimitEvent(session, paneID, agentType, waitSeconds)
			if err := m.notifier.Notify(event); err != nil {
				log.Printf("[resilience] notification error: %v", err)
			}
		}

		// Trigger rotation assistance if enabled
		if rotateConfig.Enabled && rotateConfig.AutoTrigger {
			m.triggerRotationAssistance(session, paneIndex, agentType, rotateConfig)
		}
	}()
}

// triggerRotationAssistance sends a notification with rotation command or auto-initiates rotation
func (m *Monitor) triggerRotationAssistance(session string, paneIndex int, agentType string, rotateConfig config.RotationConfig) {
	rotateCmd := fmt.Sprintf("ntm rotate %s --pane=%d", session, paneIndex)

	log.Printf("[resilience] Suggesting rotation: %s", rotateCmd)

	// Send rotation notification with command
	if m.notifier != nil {
		event := notify.NewRotationNeededEvent(session, paneIndex, agentType, rotateCmd)
		if err := m.notifier.Notify(event); err != nil {
			log.Printf("[resilience] notification error: %v", err)
		}
	}

	// Also display tmux message if in a tmux session
	if session != "" {
		msg := fmt.Sprintf("⚠️ Rate limit! Run: %s", rotateCmd)
		displayTmuxMessage(session, msg)
	}

	// Auto-initiate rotation if configured (aggressive mode)
	if rotateConfig.AutoInitiate {
		log.Printf("[resilience] Auto-initiating rotation for agent %s (pane %d)",
			agentType, paneIndex)
		// Note: Auto-initiate is disabled in this implementation because
		// rotation requires user interaction (browser account switch).
		// Instead, we just provide the notification with command.
	}
}

// displayTmuxMessage shows a message in the tmux session
func displayTmuxMessage(session, msg string) {
	// Snapshot the function under lock for thread-safe access from spawned goroutines
	hooksMu.RLock()
	fn := displayMessageFn
	hooksMu.RUnlock()

	// tmux display-message shows a message in the status line for 10 seconds
	if err := fn(session, msg, 10000); err != nil {
		log.Printf("[resilience] tmux display-message failed: %v", err)
	}
}

// handleCrash processes a detected agent crash
func (m *Monitor) handleCrash(agent *AgentState, reason string) {
	agent.Healthy = false
	agent.LastCrash = time.Now()

	log.Printf("[resilience] Agent %s (pane %d, type %s) crashed: %s",
		agent.PaneID, agent.PaneIndex, agent.AgentType, reason)

	// Snapshot values for async operations
	session := m.session
	paneID := agent.PaneID
	agentType := agent.AgentType
	notifyCrash := m.cfg.Resilience.NotifyOnCrash
	notifyMax := m.cfg.Resilience.NotifyOnMaxRestarts
	maxRestarts := m.cfg.Resilience.MaxRestarts
	currentRestarts := agent.RestartCount

	// Run notifications asynchronously
	go func() {
		// Send crash notification if enabled
		if notifyCrash && m.notifier != nil {
			event := notify.NewAgentCrashedEvent(session, paneID, agentType)
			event.Message = fmt.Sprintf("Agent %s crashed: %s", agentType, reason)
			if err := m.notifier.Notify(event); err != nil {
				log.Printf("[resilience] notification error: %v", err)
			}
		}

		// Send max restarts notification if limit reached
		if currentRestarts >= maxRestarts && notifyMax && m.notifier != nil {
			event := notify.Event{
				Type:    notify.EventAgentError,
				Session: session,
				Pane:    paneID,
				Agent:   agentType,
				Message: fmt.Sprintf("Agent %s exceeded max restart attempts (%d)",
					agentType, maxRestarts),
			}
			if err := m.notifier.Notify(event); err != nil {
				log.Printf("[resilience] notification error: %v", err)
			}
		}
	}()

	// Attempt restart if enabled and under the limit
	if m.autoRestart && agent.RestartCount < m.cfg.Resilience.MaxRestarts {
		// Schedule restart
		go m.restartAgent(agent)
	} else {
		if !m.autoRestart {
			log.Printf("[resilience] Auto-restart disabled. Agent %s stopped.", agent.PaneID)
		} else {
			log.Printf("[resilience] Agent %s exceeded max restarts (%d), not restarting",
				agent.PaneID, m.cfg.Resilience.MaxRestarts)
		}
	}
}

// restartAgent restarts a crashed agent after the configured delay
func (m *Monitor) restartAgent(agent *AgentState) {
	delay := time.Duration(m.cfg.Resilience.RestartDelaySeconds) * time.Second
	log.Printf("[resilience] Restarting agent %s in %v...", agent.PaneID, delay)

	// Snapshot hooks under lock for thread-safe access from spawned goroutines
	hooksMu.RLock()
	sleepFunc := sleepFn
	buildFunc := buildPaneCmdFn
	sendFunc := sendKeysFn
	hooksMu.RUnlock()

	sleepFunc(delay)

	m.mu.Lock()
	// Check if still in crashed state (could have been stopped)
	currentAgent, ok := m.agents[agent.PaneID]
	if !ok || currentAgent.Healthy {
		m.mu.Unlock()
		return
	}
	currentAgent.RestartCount++
	// Copy command while holding lock to avoid race
	agentCommand := currentAgent.Command
	m.mu.Unlock()

	// Re-run the agent command in the pane
	paneCmd, err := buildFunc(m.projectDir, agentCommand)
	if err != nil {
		log.Printf("[resilience] Refusing to restart agent %s: %v", agent.PaneID, err)
		return
	}

	if err := sendFunc(agent.PaneID, paneCmd, true); err != nil {
		log.Printf("[resilience] Failed to restart agent %s: %v", agent.PaneID, err)
		return
	}

	log.Printf("[resilience] Agent %s restarted (attempt %d/%d)",
		agent.PaneID, currentAgent.RestartCount, m.cfg.Resilience.MaxRestarts)

	// Send restart notification
	if m.notifier != nil {
		event := notify.Event{
			Type:    notify.EventAgentRestarted,
			Session: m.session,
			Pane:    agent.PaneID,
			Agent:   agent.AgentType,
			Message: fmt.Sprintf("Agent %s restarted (attempt %d/%d)",
				agent.AgentType, currentAgent.RestartCount, m.cfg.Resilience.MaxRestarts),
			Details: map[string]string{
				"restart_count": fmt.Sprintf("%d", currentAgent.RestartCount),
				"max_restarts":  fmt.Sprintf("%d", m.cfg.Resilience.MaxRestarts),
			},
		}
		if err := m.notifier.Notify(event); err != nil {
			log.Printf("[resilience] notification error: %v", err)
		}
	}

	// Mark as healthy again (will be rechecked on next health cycle)
	m.mu.Lock()
	if a, ok := m.agents[agent.PaneID]; ok {
		a.Healthy = true
		a.LastRestart = time.Now()
	}
	m.mu.Unlock()
}
