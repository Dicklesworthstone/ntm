package caut

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

var pollerLogger = slog.Default().With("component", "caut.poller")

// UsagePoller manages background polling of caut for usage data.
type UsagePoller struct {
	mu          sync.RWMutex
	lifecycleMu sync.Mutex

	adapter  *tools.CautAdapter
	cache    *UsageCache
	config   *config.CautConfig
	alerter  *alerts.Tracker
	interval time.Duration

	doneCh chan struct{}
	cancel context.CancelFunc

	running bool
}

// NewUsagePoller creates a new usage poller.
func NewUsagePoller(cfg *config.CautConfig, alerter *alerts.Tracker) *UsagePoller {
	interval := time.Duration(cfg.PollInterval) * time.Second
	if interval < 10*time.Second {
		interval = 60 * time.Second // Minimum 10 seconds, default to 60
	}

	return &UsagePoller{
		adapter:  tools.NewCautAdapter(),
		cache:    NewUsageCache(),
		config:   cfg,
		alerter:  alerter,
		interval: interval,
	}
}

// Start begins background polling. It is safe to call multiple times;
// subsequent calls are no-ops if already running.
func (p *UsagePoller) Start() error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()

	p.mu.RLock()
	if p.running {
		p.mu.RUnlock()
		return nil
	}
	p.mu.RUnlock()

	// Check if caut is available before starting.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !p.adapter.IsAvailable(ctx) {
		return fmt.Errorf("caut is not available: binary not found or incompatible version")
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.running = true
	p.cancel = runCancel
	p.doneCh = make(chan struct{})
	doneCh := p.doneCh
	p.mu.Unlock()

	go func() {
		defer runCancel()
		p.pollLoop(runCtx, doneCh)
	}()

	pollerLogger.Info("caut usage poller started", "interval", p.interval)
	return nil
}

// Stop halts background polling. It blocks until the polling goroutine exits.
func (p *UsagePoller) Stop() {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()

	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	cancel := p.cancel
	doneCh := p.doneCh
	p.running = false
	p.cancel = nil
	p.doneCh = nil
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if doneCh != nil {
		<-doneCh
	}

	pollerLogger.Info("caut usage poller stopped")
}

// IsRunning returns true if the poller is currently running.
func (p *UsagePoller) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

// GetCache returns the usage cache for reading cached data.
func (p *UsagePoller) GetCache() *UsageCache {
	return p.cache
}

// SetInterval updates the polling interval. Takes effect on the next poll cycle.
func (p *UsagePoller) SetInterval(interval time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	p.interval = interval
	pollerLogger.Info("polling interval updated", "interval", interval)
}

// PollNow triggers an immediate poll, bypassing the interval timer.
// Returns the result of the poll operation.
func (p *UsagePoller) PollNow(ctx context.Context) error {
	return p.poll(ctx)
}

// pollLoop runs the background polling loop.
func (p *UsagePoller) pollLoop(ctx context.Context, doneCh chan struct{}) {
	defer close(doneCh)

	// Initial poll immediately.
	if err := p.pollWithTimeout(ctx); err != nil {
		if ctx.Err() == nil {
			pollerLogger.Warn("initial caut poll failed", "error", err)
		}
	}

	p.mu.RLock()
	currentInterval := p.interval
	p.mu.RUnlock()
	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.pollWithTimeout(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				pollerLogger.Warn("caut poll failed", "error", err)
				p.cache.SetError(err)
			} else {
				p.cache.ClearError()
			}

			// Update ticker if interval changed
			p.mu.RLock()
			currentInterval = p.interval
			p.mu.RUnlock()
			ticker.Reset(currentInterval)
		}
	}
}

func (p *UsagePoller) pollWithTimeout(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	return p.poll(ctx)
}

// poll fetches current usage data and updates the cache.
func (p *UsagePoller) poll(ctx context.Context) error {
	pollerLogger.Debug("polling caut for usage data")

	// Fetch status
	status, err := p.adapter.GetStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get caut status: %w", err)
	}

	p.cache.UpdateStatus(status)

	// Fetch all usage data
	usages, err := p.adapter.GetAllUsage(ctx, "day")
	if err != nil {
		pollerLogger.Warn("failed to get usage data", "error", err)
		// Don't fail the whole poll if usage fetch fails
	} else {
		p.cache.UpdateAllUsage(usages)
	}

	// Check for alerts
	p.checkAlerts(status)

	pollerLogger.Debug("caut poll complete",
		"providers", status.ProviderCount,
		"quota_percent", status.QuotaPercent,
		"total_spend", status.TotalSpend,
	)

	return nil
}

// checkAlerts evaluates quota thresholds and triggers alerts if needed.
func (p *UsagePoller) checkAlerts(status *tools.CautStatus) {
	if p.alerter == nil || status == nil {
		return
	}

	threshold := float64(p.config.AlertThreshold)
	criticalThreshold := 95.0 // Fixed critical threshold at 95%

	// Check overall quota
	if status.QuotaPercent >= criticalThreshold {
		alert := alerts.Alert{
			ID:       "caut-quota-critical-overall",
			Type:     alerts.AlertQuotaCritical,
			Severity: alerts.SeverityCritical,
			Source:   "caut-poller",
			Message:  fmt.Sprintf("API quota critical: %.1f%% used (threshold: %.1f%%)", status.QuotaPercent, criticalThreshold),
			Context: map[string]interface{}{
				"quota_percent":  status.QuotaPercent,
				"threshold":      criticalThreshold,
				"total_spend":    status.TotalSpend,
				"provider_count": status.ProviderCount,
			},
		}
		p.alerter.AddAlert(alert)
		pollerLogger.Warn("quota critical alert triggered", "quota_percent", status.QuotaPercent)
	} else if status.QuotaPercent >= threshold {
		alert := alerts.Alert{
			ID:       "caut-quota-warning-overall",
			Type:     alerts.AlertQuotaWarning,
			Severity: alerts.SeverityWarning,
			Source:   "caut-poller",
			Message:  fmt.Sprintf("API quota warning: %.1f%% used (threshold: %.1f%%)", status.QuotaPercent, threshold),
			Context: map[string]interface{}{
				"quota_percent":  status.QuotaPercent,
				"threshold":      threshold,
				"total_spend":    status.TotalSpend,
				"provider_count": status.ProviderCount,
			},
		}
		p.alerter.AddAlert(alert)
		pollerLogger.Info("quota warning alert triggered", "quota_percent", status.QuotaPercent)
	}

	// Check per-provider quotas
	for _, provider := range status.Providers {
		if !provider.Enabled || !provider.HasQuota {
			continue
		}

		if provider.QuotaUsed >= criticalThreshold {
			alert := alerts.Alert{
				ID:       fmt.Sprintf("caut-quota-critical-%s", provider.Name),
				Type:     alerts.AlertQuotaCritical,
				Severity: alerts.SeverityCritical,
				Source:   "caut-poller",
				Message:  fmt.Sprintf("%s API quota critical: %.1f%% used", provider.Name, provider.QuotaUsed),
				Context: map[string]interface{}{
					"provider":      provider.Name,
					"quota_percent": provider.QuotaUsed,
					"threshold":     criticalThreshold,
				},
			}
			p.alerter.AddAlert(alert)
		} else if provider.QuotaUsed >= threshold {
			alert := alerts.Alert{
				ID:       fmt.Sprintf("caut-quota-warning-%s", provider.Name),
				Type:     alerts.AlertQuotaWarning,
				Severity: alerts.SeverityWarning,
				Source:   "caut-poller",
				Message:  fmt.Sprintf("%s API quota warning: %.1f%% used", provider.Name, provider.QuotaUsed),
				Context: map[string]interface{}{
					"provider":      provider.Name,
					"quota_percent": provider.QuotaUsed,
					"threshold":     threshold,
				},
			}
			p.alerter.AddAlert(alert)
		}
	}
}

// Global poller instance management

var (
	globalPoller     *UsagePoller
	globalPollerOnce sync.Once
	globalPollerMu   sync.RWMutex
)

// GetGlobalPoller returns the global caut usage poller singleton.
// Creates one with default config if not initialized.
func GetGlobalPoller() *UsagePoller {
	globalPollerOnce.Do(func() {
		cfg := config.DefaultCautConfig()
		globalPoller = NewUsagePoller(&cfg, alerts.GetGlobalTracker())
	})
	return globalPoller
}

// InitGlobalPoller initializes the global poller with specific config.
// Should be called early in application startup before GetGlobalPoller.
func InitGlobalPoller(cfg *config.CautConfig, alerter *alerts.Tracker) *UsagePoller {
	globalPollerMu.Lock()
	defer globalPollerMu.Unlock()

	if globalPoller != nil {
		globalPoller.Stop()
	}

	globalPoller = NewUsagePoller(cfg, alerter)
	return globalPoller
}

// StartGlobalPoller starts the global poller if not already running.
func StartGlobalPoller() error {
	return GetGlobalPoller().Start()
}

// StopGlobalPoller stops the global poller if running.
func StopGlobalPoller() {
	globalPollerMu.RLock()
	poller := globalPoller
	globalPollerMu.RUnlock()

	if poller != nil {
		poller.Stop()
	}
}
