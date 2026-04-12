// Package integrations provides coordination between external tool integrations.
package integrations

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/integrations/caut"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

var coordinatorLogger = slog.Default().With("component", "integrations.coordinator")

// Coordinator manages interactions between caut and CAAM integrations.
// It monitors caut alerts for high usage and proactively triggers
// CAAM account switches before hitting actual rate limits.
type Coordinator struct {
	mu sync.RWMutex

	lifecycleMu sync.Mutex

	caamAdapter  *tools.CAAMAdapter
	cautPoller   *caut.UsagePoller
	alertTracker *alerts.Tracker
	config       *CoordinatorConfig

	// Track recent switches to prevent thrashing
	lastSwitch     map[string]time.Time // provider -> last switch time
	switchCooldown time.Duration

	// Callbacks for switch events
	onSwitch SwitchCallback

	stopCh chan struct{}
	doneCh chan struct{}

	running bool
}

// CoordinatorConfig holds configuration for the coordinator.
type CoordinatorConfig struct {
	// CAAMEnabled indicates if CAAM integration is available
	CAAMEnabled bool
	// CautEnabled indicates if caut integration is available
	CautEnabled bool
	// AutoRotate enables automatic CAAM rotation on high caut usage
	AutoRotate bool
	// ProactiveThreshold is the usage percentage to trigger pre-emptive switch (0-100)
	ProactiveThreshold float64
	// SwitchCooldown is minimum time between switches for the same provider
	SwitchCooldown time.Duration
	// CheckInterval is how often to check for alerts
	CheckInterval time.Duration
}

// DefaultCoordinatorConfig returns sensible defaults.
func DefaultCoordinatorConfig() CoordinatorConfig {
	return CoordinatorConfig{
		CAAMEnabled:        true,
		CautEnabled:        true,
		AutoRotate:         true,
		ProactiveThreshold: 90.0, // Switch at 90% to avoid hitting limit
		SwitchCooldown:     5 * time.Minute,
		CheckInterval:      30 * time.Second,
	}
}

// SwitchCallback is called when an account switch occurs.
type SwitchCallback func(event SwitchEvent)

// SwitchEvent contains details about a proactive account switch.
type SwitchEvent struct {
	Provider          string    `json:"provider"`
	UsagePercent      float64   `json:"usage_percent"`
	Reason            string    `json:"reason"` // "proactive" or "rate_limit"
	PreviousAccount   string    `json:"previous_account,omitempty"`
	NewAccount        string    `json:"new_account,omitempty"`
	Success           bool      `json:"success"`
	Error             string    `json:"error,omitempty"`
	Timestamp         time.Time `json:"timestamp"`
	AccountsRemaining int       `json:"accounts_remaining,omitempty"`
}

// NewCoordinator creates a new integration coordinator.
func NewCoordinator(cfg *CoordinatorConfig) *Coordinator {
	if cfg == nil {
		defaultCfg := DefaultCoordinatorConfig()
		cfg = &defaultCfg
	}

	return &Coordinator{
		caamAdapter:    tools.NewCAAMAdapter(),
		cautPoller:     caut.GetGlobalPoller(),
		alertTracker:   alerts.GetGlobalTracker(),
		config:         cfg,
		lastSwitch:     make(map[string]time.Time),
		switchCooldown: cfg.SwitchCooldown,
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
	}
}

// NewCoordinatorFromConfig creates a coordinator from NTM config.
func NewCoordinatorFromConfig(intCfg *config.IntegrationsConfig) *Coordinator {
	cfg := CoordinatorConfig{
		CAAMEnabled:        intCfg.CAAM.Enabled,
		CautEnabled:        intCfg.Caut.Enabled,
		AutoRotate:         intCfg.CAAM.AutoRotate,
		ProactiveThreshold: 90.0, // Fixed at 90% for proactive switching
		SwitchCooldown:     time.Duration(intCfg.CAAM.AccountCooldown) * time.Second,
		CheckInterval:      30 * time.Second,
	}

	return NewCoordinator(&cfg)
}

// SetSwitchCallback sets a callback to be invoked on account switches.
func (c *Coordinator) SetSwitchCallback(cb SwitchCallback) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onSwitch = cb
}

// Start begins monitoring for proactive switching opportunities.
func (c *Coordinator) Start() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return nil
	}

	// Verify both integrations are available
	if !c.config.CAAMEnabled || !c.config.CautEnabled {
		coordinatorLogger.Info("coordinator not starting: integrations not enabled",
			"caam_enabled", c.config.CAAMEnabled,
			"caut_enabled", c.config.CautEnabled,
		)
		return nil
	}

	// Check if CAAM is actually available
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !c.caamAdapter.IsAvailable(ctx) {
		coordinatorLogger.Info("coordinator not starting: CAAM not available")
		return nil
	}

	if !c.caamAdapter.HasMultipleAccounts(ctx) {
		coordinatorLogger.Info("coordinator not starting: CAAM has only one account (no rotation possible)")
		return nil
	}

	c.running = true
	c.stopCh = make(chan struct{})
	c.doneCh = make(chan struct{})

	go c.monitorLoop()

	coordinatorLogger.Info("caut-CAAM coordinator started",
		"proactive_threshold", c.config.ProactiveThreshold,
		"auto_rotate", c.config.AutoRotate,
	)

	return nil
}

// Stop halts the coordinator.
func (c *Coordinator) Stop() {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	stopCh := c.stopCh
	doneCh := c.doneCh
	c.mu.Unlock()

	close(stopCh)
	<-doneCh

	coordinatorLogger.Info("caut-CAAM coordinator stopped")
}

// IsRunning returns true if the coordinator is active.
func (c *Coordinator) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running
}

// OnCautAlert handles alerts from the caut poller.
// This is the main entry point for proactive switching.
func (c *Coordinator) OnCautAlert(provider string, usagePercent float64) {
	c.mu.RLock()
	autoRotate := c.config.AutoRotate
	threshold := c.config.ProactiveThreshold
	c.mu.RUnlock()

	if !autoRotate {
		coordinatorLogger.Debug("auto-rotate disabled, skipping proactive switch",
			"provider", provider,
			"usage_percent", usagePercent,
		)
		return
	}

	if usagePercent < threshold {
		return // Not at threshold yet
	}

	// Check cooldown
	if c.inCooldown(provider) {
		coordinatorLogger.Debug("provider in cooldown, skipping switch",
			"provider", provider,
			"usage_percent", usagePercent,
		)
		return
	}

	// Trigger proactive switch
	c.triggerSwitch(provider, usagePercent, "proactive")
}

// triggerSwitch performs the actual account switch.
func (c *Coordinator) triggerSwitch(provider string, usagePercent float64, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	event := SwitchEvent{
		Provider:     provider,
		UsagePercent: usagePercent,
		Reason:       reason,
		Timestamp:    time.Now(),
	}

	coordinatorLogger.Info("triggering proactive account switch",
		"provider", provider,
		"usage_percent", usagePercent,
		"reason", reason,
	)

	// Perform the switch
	result, err := c.caamAdapter.SwitchToNextAccount(ctx, provider)
	if err != nil {
		event.Success = false
		event.Error = err.Error()
		coordinatorLogger.Warn("proactive account switch failed",
			"provider", provider,
			"error", err,
		)

		// Add alert for failed switch
		c.alertTracker.AddAlert(alerts.Alert{
			ID:       "caut-caam-switch-failed-" + provider,
			Type:     alerts.AlertRateLimit,
			Severity: alerts.SeverityWarning,
			Source:   "coordinator",
			Message:  "Proactive account switch failed for " + provider + ": " + err.Error(),
			Context: map[string]interface{}{
				"provider":      provider,
				"usage_percent": usagePercent,
				"reason":        reason,
			},
		})
	} else {
		event.Success = true
		event.PreviousAccount = result.PreviousAccount
		event.NewAccount = result.NewAccount
		event.AccountsRemaining = result.AccountsRemaining

		// Record switch time for cooldown tracking
		c.recordSwitch(provider)

		coordinatorLogger.Info("proactive account switch successful",
			"provider", provider,
			"previous_account", result.PreviousAccount,
			"new_account", result.NewAccount,
			"accounts_remaining", result.AccountsRemaining,
		)

		// Add informational alert for successful switch
		c.alertTracker.AddAlert(alerts.Alert{
			ID:       "caut-caam-switch-" + provider,
			Type:     alerts.AlertQuotaWarning,
			Severity: alerts.SeverityInfo,
			Source:   "coordinator",
			Message:  "Proactively switched " + provider + " account from " + result.PreviousAccount + " to " + result.NewAccount,
			Context: map[string]interface{}{
				"provider":           provider,
				"usage_percent":      usagePercent,
				"previous_account":   result.PreviousAccount,
				"new_account":        result.NewAccount,
				"accounts_remaining": result.AccountsRemaining,
			},
		})
	}

	// Invoke callback if set
	c.mu.RLock()
	cb := c.onSwitch
	c.mu.RUnlock()

	if cb != nil {
		cb(event)
	}
}

// inCooldown checks if a provider is in switch cooldown.
func (c *Coordinator) inCooldown(provider string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	lastTime, ok := c.lastSwitch[provider]
	if !ok {
		return false
	}

	return time.Since(lastTime) < c.switchCooldown
}

// recordSwitch records when a switch occurred for cooldown tracking.
func (c *Coordinator) recordSwitch(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSwitch[provider] = time.Now()
}

// monitorLoop periodically checks caut data for high usage.
func (c *Coordinator) monitorLoop() {
	defer close(c.doneCh)

	ticker := time.NewTicker(c.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.checkUsageForProactiveSwitch()

		case <-c.stopCh:
			return
		}
	}
}

// checkUsageForProactiveSwitch examines cached caut data for high usage.
func (c *Coordinator) checkUsageForProactiveSwitch() {
	cache := c.cautPoller.GetCache()
	if cache == nil {
		return
	}

	status := cache.GetStatus()
	if status == nil {
		return
	}

	threshold := c.config.ProactiveThreshold

	// Check per-provider quotas
	for _, provider := range status.Providers {
		if !provider.Enabled || !provider.HasQuota {
			continue
		}

		if provider.QuotaUsed >= threshold {
			c.OnCautAlert(provider.Name, provider.QuotaUsed)
		}
	}
}

// GetLastSwitchTime returns when the last switch occurred for a provider.
func (c *Coordinator) GetLastSwitchTime(provider string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	t, ok := c.lastSwitch[provider]
	return t, ok
}

// ResetCooldown clears the cooldown for a provider (for testing or manual override).
func (c *Coordinator) ResetCooldown(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.lastSwitch, provider)
}

// Global coordinator instance

var (
	globalCoordinator     *Coordinator
	globalCoordinatorOnce sync.Once
	globalCoordinatorMu   sync.RWMutex
)

// GetGlobalCoordinator returns the global coordinator singleton.
func GetGlobalCoordinator() *Coordinator {
	globalCoordinatorOnce.Do(func() {
		cfg := DefaultCoordinatorConfig()
		globalCoordinator = NewCoordinator(&cfg)
	})
	return globalCoordinator
}

// InitGlobalCoordinator initializes the global coordinator with specific config.
func InitGlobalCoordinator(cfg *CoordinatorConfig) *Coordinator {
	globalCoordinatorMu.Lock()
	defer globalCoordinatorMu.Unlock()

	if globalCoordinator != nil {
		globalCoordinator.Stop()
	}

	globalCoordinator = NewCoordinator(cfg)
	return globalCoordinator
}

// StartGlobalCoordinator starts the global coordinator if not already running.
func StartGlobalCoordinator() error {
	return GetGlobalCoordinator().Start()
}

// StopGlobalCoordinator stops the global coordinator if running.
func StopGlobalCoordinator() {
	globalCoordinatorMu.RLock()
	coord := globalCoordinator
	globalCoordinatorMu.RUnlock()

	if coord != nil {
		coord.Stop()
	}
}
