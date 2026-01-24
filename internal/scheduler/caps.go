package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// AgentCapConfig configures concurrency caps for a specific agent type.
type AgentCapConfig struct {
	// MaxConcurrent is the maximum number of concurrent instances.
	MaxConcurrent int `json:"max_concurrent"`

	// RampUpEnabled enables gradual capacity increase over time.
	RampUpEnabled bool `json:"ramp_up_enabled"`

	// RampUpInitial is the starting cap when ramp-up is enabled.
	RampUpInitial int `json:"ramp_up_initial"`

	// RampUpStep is how much to increase the cap each interval.
	RampUpStep int `json:"ramp_up_step"`

	// RampUpInterval is how often to increase the cap.
	RampUpInterval time.Duration `json:"ramp_up_interval"`

	// CooldownOnFailure reduces the cap on failure.
	CooldownOnFailure bool `json:"cooldown_on_failure"`

	// CooldownReduction is how much to reduce on failure.
	CooldownReduction int `json:"cooldown_reduction"`

	// CooldownRecovery is how long before restoring cap after cooldown.
	CooldownRecovery time.Duration `json:"cooldown_recovery"`
}

// DefaultAgentCapConfig returns sensible defaults for agent caps.
func DefaultAgentCapConfig() AgentCapConfig {
	return AgentCapConfig{
		MaxConcurrent:     4,
		RampUpEnabled:     false,
		RampUpInitial:     1,
		RampUpStep:        1,
		RampUpInterval:    30 * time.Second,
		CooldownOnFailure: true,
		CooldownReduction: 1,
		CooldownRecovery:  60 * time.Second,
	}
}

// CodexCapConfig returns Codex-specific conservative defaults.
func CodexCapConfig() AgentCapConfig {
	return AgentCapConfig{
		MaxConcurrent:     3,                // Conservative max
		RampUpEnabled:     true,             // Start slow
		RampUpInitial:     1,                // Start with 1 cod
		RampUpStep:        1,                // Add 1 at a time
		RampUpInterval:    60 * time.Second, // Slow ramp-up
		CooldownOnFailure: true,
		CooldownReduction: 1,
		CooldownRecovery:  120 * time.Second, // Long recovery
	}
}

// AgentCapsConfig contains configuration for all agent type caps.
type AgentCapsConfig struct {
	// Default is the default cap config for unknown agent types.
	Default AgentCapConfig `json:"default"`

	// PerAgent contains per-agent-type overrides.
	PerAgent map[string]AgentCapConfig `json:"per_agent,omitempty"`

	// GlobalMax is the absolute maximum across all agents (0 = no limit).
	GlobalMax int `json:"global_max"`
}

// DefaultAgentCapsConfig returns sensible defaults for agent caps.
func DefaultAgentCapsConfig() AgentCapsConfig {
	return AgentCapsConfig{
		Default:   DefaultAgentCapConfig(),
		GlobalMax: 0, // No global cap by default
		PerAgent: map[string]AgentCapConfig{
			"cc":  DefaultAgentCapConfig(), // Claude: standard caps
			"cod": CodexCapConfig(),        // Codex: conservative + ramp-up
			"gmi": {                         // Gemini: slightly higher
				MaxConcurrent:     5,
				RampUpEnabled:     false,
				CooldownOnFailure: true,
				CooldownReduction: 1,
				CooldownRecovery:  60 * time.Second,
			},
		},
	}
}

// AgentCaps manages per-agent concurrency caps with ramp-up and cooldown.
type AgentCaps struct {
	mu sync.Mutex

	config AgentCapsConfig

	// running tracks current running count per agent type.
	running map[string]int

	// caps tracks dynamic current caps per agent type.
	caps map[string]*agentCapState

	// waiters tracks goroutines waiting for capacity.
	waiters map[string][]chan struct{}

	// stats tracks cap statistics.
	stats CapsStats
}

// agentCapState tracks the state of caps for one agent type.
type agentCapState struct {
	config     AgentCapConfig
	currentCap int       // Current effective cap (may differ from max due to ramp-up/cooldown)
	startedAt  time.Time // When this agent type first started (for ramp-up)
	lastRampUp time.Time // When cap was last increased
	cooldownAt time.Time // When cooldown was triggered
	inCooldown bool      // Whether currently in cooldown
}

// CapsStats contains cap statistics.
type CapsStats struct {
	// PerAgent contains per-agent statistics.
	PerAgent map[string]AgentCapStats `json:"per_agent"`

	// TotalRunning is the total running across all agents.
	TotalRunning int `json:"total_running"`

	// TotalWaiting is the total waiting across all agents.
	TotalWaiting int `json:"total_waiting"`
}

// AgentCapStats contains statistics for one agent type.
type AgentCapStats struct {
	Running    int  `json:"running"`     // Currently running instances
	CurrentCap int  `json:"current_cap"` // Current effective cap
	MaxCap     int  `json:"max_cap"`     // Configured max cap
	Waiting    int  `json:"waiting"`     // Waiting for capacity
	InRampUp   bool `json:"in_ramp_up"`  // Currently ramping up
	InCooldown bool `json:"in_cooldown"` // Currently in cooldown
}

// NewAgentCaps creates a new agent caps manager.
func NewAgentCaps(cfg AgentCapsConfig) *AgentCaps {
	ac := &AgentCaps{
		config:  cfg,
		running: make(map[string]int),
		caps:    make(map[string]*agentCapState),
		waiters: make(map[string][]chan struct{}),
		stats: CapsStats{
			PerAgent: make(map[string]AgentCapStats),
		},
	}

	// Pre-initialize configured agent caps
	for agent, capCfg := range cfg.PerAgent {
		ac.caps[agent] = &agentCapState{
			config:     capCfg,
			currentCap: ac.initialCap(capCfg),
		}
	}

	return ac
}

// initialCap returns the initial cap based on config.
func (ac *AgentCaps) initialCap(cfg AgentCapConfig) int {
	if cfg.RampUpEnabled {
		return cfg.RampUpInitial
	}
	return cfg.MaxConcurrent
}

// getCapState returns or creates cap state for an agent type.
func (ac *AgentCaps) getCapState(agentType string) *agentCapState {
	state, ok := ac.caps[agentType]
	if ok {
		return state
	}

	// Create state from config
	cfg, ok := ac.config.PerAgent[agentType]
	if !ok {
		cfg = ac.config.Default
	}

	state = &agentCapState{
		config:     cfg,
		currentCap: ac.initialCap(cfg),
	}
	ac.caps[agentType] = state
	return state
}

// TryAcquire tries to acquire a slot without blocking.
// Returns true if acquired, false if at capacity.
func (ac *AgentCaps) TryAcquire(agentType string) bool {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	state := ac.getCapState(agentType)
	ac.updateRampUp(agentType, state)

	// Check if at capacity
	if ac.running[agentType] >= state.currentCap {
		return false
	}

	// Check global cap
	if ac.config.GlobalMax > 0 {
		total := 0
		for _, count := range ac.running {
			total += count
		}
		if total >= ac.config.GlobalMax {
			return false
		}
	}

	ac.running[agentType]++
	ac.stats.TotalRunning++

	// Start ramp-up timer if this is the first instance
	if state.startedAt.IsZero() {
		state.startedAt = time.Now()
		state.lastRampUp = time.Now()
	}

	slog.Debug("agent cap acquired",
		"agent_type", agentType,
		"running", ac.running[agentType],
		"cap", state.currentCap,
	)

	return true
}

// Acquire blocks until a slot is available or context is cancelled.
func (ac *AgentCaps) Acquire(ctx context.Context, agentType string) error {
	// First try without blocking
	if ac.TryAcquire(agentType) {
		return nil
	}

	// Need to wait
	ac.mu.Lock()

	// Double-check after lock
	state := ac.getCapState(agentType)
	ac.updateRampUp(agentType, state)

	if ac.running[agentType] < state.currentCap && !ac.globalCapExceeded() {
		ac.running[agentType]++
		ac.stats.TotalRunning++
		if state.startedAt.IsZero() {
			state.startedAt = time.Now()
			state.lastRampUp = time.Now()
		}
		ac.mu.Unlock()
		return nil
	}

	// Create wait channel
	waitCh := make(chan struct{}, 1)
	ac.waiters[agentType] = append(ac.waiters[agentType], waitCh)
	ac.stats.TotalWaiting++

	ac.mu.Unlock()

	slog.Debug("agent cap waiting",
		"agent_type", agentType,
		"running", ac.running[agentType],
		"cap", state.currentCap,
	)

	// Wait for slot
	select {
	case <-ctx.Done():
		ac.mu.Lock()
		ac.removeWaiter(agentType, waitCh)
		ac.stats.TotalWaiting--
		ac.mu.Unlock()
		return ctx.Err()
	case <-waitCh:
		return nil
	}
}

// globalCapExceeded checks if global cap is exceeded.
func (ac *AgentCaps) globalCapExceeded() bool {
	if ac.config.GlobalMax <= 0 {
		return false
	}
	total := 0
	for _, count := range ac.running {
		total += count
	}
	return total >= ac.config.GlobalMax
}

// removeWaiter removes a waiter channel from the list.
func (ac *AgentCaps) removeWaiter(agentType string, ch chan struct{}) {
	waiters := ac.waiters[agentType]
	for i, w := range waiters {
		if w == ch {
			ac.waiters[agentType] = append(waiters[:i], waiters[i+1:]...)
			return
		}
	}
}

// Release releases a slot for an agent type.
func (ac *AgentCaps) Release(agentType string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.running[agentType] > 0 {
		ac.running[agentType]--
		ac.stats.TotalRunning--
	}

	slog.Debug("agent cap released",
		"agent_type", agentType,
		"running", ac.running[agentType],
	)

	// Notify one waiter if any
	ac.notifyWaiter(agentType)
}

// notifyWaiter notifies one waiting goroutine that a slot is available.
func (ac *AgentCaps) notifyWaiter(agentType string) {
	// First try the specific agent type
	if len(ac.waiters[agentType]) > 0 {
		waiter := ac.waiters[agentType][0]
		ac.waiters[agentType] = ac.waiters[agentType][1:]
		ac.stats.TotalWaiting--

		// Mark slot as acquired for the waiter
		ac.running[agentType]++
		ac.stats.TotalRunning++

		select {
		case waiter <- struct{}{}:
		default:
		}
		return
	}

	// If global cap was the blocker, try notifying any agent type
	if ac.config.GlobalMax > 0 {
		for agent, waiters := range ac.waiters {
			if len(waiters) > 0 {
				state := ac.getCapState(agent)
				if ac.running[agent] < state.currentCap {
					waiter := waiters[0]
					ac.waiters[agent] = waiters[1:]
					ac.stats.TotalWaiting--
					ac.running[agent]++
					ac.stats.TotalRunning++
					select {
					case waiter <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}
}

// RecordFailure records a failure for an agent type, potentially triggering cooldown.
func (ac *AgentCaps) RecordFailure(agentType string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	state := ac.getCapState(agentType)
	if !state.config.CooldownOnFailure {
		return
	}

	// Reduce cap
	if state.currentCap > 1 {
		state.currentCap -= state.config.CooldownReduction
		if state.currentCap < 1 {
			state.currentCap = 1
		}
	}

	state.inCooldown = true
	state.cooldownAt = time.Now()

	slog.Warn("agent cap reduced due to failure",
		"agent_type", agentType,
		"new_cap", state.currentCap,
		"cooldown_recovery", state.config.CooldownRecovery,
	)

	// Schedule recovery
	go func(agent string, recovery time.Duration) {
		time.Sleep(recovery)
		ac.recoverFromCooldown(agent)
	}(agentType, state.config.CooldownRecovery)
}

// recoverFromCooldown restores cap after cooldown period.
func (ac *AgentCaps) recoverFromCooldown(agentType string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	state, ok := ac.caps[agentType]
	if !ok || !state.inCooldown {
		return
	}

	// Only recover if no failures since cooldown started
	if time.Since(state.cooldownAt) >= state.config.CooldownRecovery {
		state.inCooldown = false

		// Restore cap (considering ramp-up state)
		if state.config.RampUpEnabled {
			ac.updateRampUp(agentType, state)
		} else {
			state.currentCap = state.config.MaxConcurrent
		}

		slog.Info("agent cap recovered from cooldown",
			"agent_type", agentType,
			"restored_cap", state.currentCap,
		)

		// Notify any waiters
		ac.notifyWaiter(agentType)
	}
}

// RecordSuccess records a successful spawn for an agent type.
func (ac *AgentCaps) RecordSuccess(agentType string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	state := ac.getCapState(agentType)

	// If in cooldown, schedule recovery check
	if state.inCooldown {
		// Success during cooldown - reset cooldown timer
		state.cooldownAt = time.Now().Add(-state.config.CooldownRecovery)
	}
}

// updateRampUp checks and applies ramp-up if needed.
func (ac *AgentCaps) updateRampUp(agentType string, state *agentCapState) {
	if !state.config.RampUpEnabled || state.startedAt.IsZero() {
		return
	}

	// Check if it's time to ramp up
	if state.currentCap < state.config.MaxConcurrent {
		elapsed := time.Since(state.lastRampUp)
		if elapsed >= state.config.RampUpInterval {
			// Calculate how many steps to increase
			steps := int(elapsed / state.config.RampUpInterval)
			increase := steps * state.config.RampUpStep

			newCap := state.currentCap + increase
			if newCap > state.config.MaxConcurrent {
				newCap = state.config.MaxConcurrent
			}

			if newCap > state.currentCap {
				oldCap := state.currentCap
				state.currentCap = newCap
				state.lastRampUp = time.Now()

				slog.Info("agent cap ramped up",
					"agent_type", agentType,
					"old_cap", oldCap,
					"new_cap", state.currentCap,
					"max_cap", state.config.MaxConcurrent,
				)

				// Notify waiters about new capacity
				for state.currentCap > ac.running[agentType] && len(ac.waiters[agentType]) > 0 {
					ac.notifyWaiter(agentType)
				}
			}
		}
	}
}

// GetRunning returns the current running count for an agent type.
func (ac *AgentCaps) GetRunning(agentType string) int {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.running[agentType]
}

// GetCurrentCap returns the current effective cap for an agent type.
func (ac *AgentCaps) GetCurrentCap(agentType string) int {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	state := ac.getCapState(agentType)
	ac.updateRampUp(agentType, state)
	return state.currentCap
}

// GetAvailable returns available slots for an agent type.
func (ac *AgentCaps) GetAvailable(agentType string) int {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	state := ac.getCapState(agentType)
	ac.updateRampUp(agentType, state)
	return state.currentCap - ac.running[agentType]
}

// Stats returns cap statistics.
func (ac *AgentCaps) Stats() CapsStats {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	stats := CapsStats{
		PerAgent:     make(map[string]AgentCapStats),
		TotalRunning: ac.stats.TotalRunning,
		TotalWaiting: ac.stats.TotalWaiting,
	}

	// Include all configured agent types
	for agentType := range ac.config.PerAgent {
		state := ac.getCapState(agentType)
		ac.updateRampUp(agentType, state)
		stats.PerAgent[agentType] = AgentCapStats{
			Running:    ac.running[agentType],
			CurrentCap: state.currentCap,
			MaxCap:     state.config.MaxConcurrent,
			Waiting:    len(ac.waiters[agentType]),
			InRampUp:   state.config.RampUpEnabled && state.currentCap < state.config.MaxConcurrent,
			InCooldown: state.inCooldown,
		}
	}

	// Include any runtime-added types
	for agentType := range ac.running {
		if _, ok := stats.PerAgent[agentType]; !ok {
			state := ac.getCapState(agentType)
			stats.PerAgent[agentType] = AgentCapStats{
				Running:    ac.running[agentType],
				CurrentCap: state.currentCap,
				MaxCap:     state.config.MaxConcurrent,
				Waiting:    len(ac.waiters[agentType]),
				InRampUp:   state.config.RampUpEnabled && state.currentCap < state.config.MaxConcurrent,
				InCooldown: state.inCooldown,
			}
		}
	}

	return stats
}

// SetCap dynamically updates the cap for an agent type.
func (ac *AgentCaps) SetCap(agentType string, cap int) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	state := ac.getCapState(agentType)
	oldCap := state.currentCap
	state.config.MaxConcurrent = cap

	// Adjust current cap to new limit
	if cap > oldCap && !state.config.RampUpEnabled {
		// Increase current cap to new max (if not using ramp-up)
		state.currentCap = cap
	} else if state.currentCap > cap {
		// Decrease current cap if over new limit
		state.currentCap = cap
	}

	slog.Info("agent cap updated",
		"agent_type", agentType,
		"new_max_cap", cap,
		"current_cap", state.currentCap,
	)

	// Notify waiters if cap increased
	if state.currentCap > oldCap {
		for state.currentCap > ac.running[agentType] && len(ac.waiters[agentType]) > 0 {
			ac.notifyWaiter(agentType)
		}
	}
}

// ForceRampUp immediately increases cap to max for an agent type.
func (ac *AgentCaps) ForceRampUp(agentType string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	state := ac.getCapState(agentType)
	if state.currentCap < state.config.MaxConcurrent {
		oldCap := state.currentCap
		state.currentCap = state.config.MaxConcurrent
		state.inCooldown = false

		slog.Info("agent cap force ramped up",
			"agent_type", agentType,
			"old_cap", oldCap,
			"new_cap", state.currentCap,
		)

		// Notify waiters
		for state.currentCap > ac.running[agentType] && len(ac.waiters[agentType]) > 0 {
			ac.notifyWaiter(agentType)
		}
	}
}

// Reset resets all cap states to initial values.
func (ac *AgentCaps) Reset() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	ac.running = make(map[string]int)
	ac.caps = make(map[string]*agentCapState)
	ac.stats = CapsStats{PerAgent: make(map[string]AgentCapStats)}

	// Re-initialize from config
	for agent, capCfg := range ac.config.PerAgent {
		ac.caps[agent] = &agentCapState{
			config:     capCfg,
			currentCap: ac.initialCap(capCfg),
		}
	}

	// Notify and clear all waiters
	for agent, waiters := range ac.waiters {
		for _, ch := range waiters {
			close(ch)
		}
		ac.waiters[agent] = nil
	}
}
