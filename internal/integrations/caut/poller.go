package caut

import (
	"context"
	"errors"
	"sync"
	"time"

	cautcli "github.com/Dicklesworthstone/ntm/internal/caut"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// MaxAge is how old cached caut usage may be before a reader refreshes it.
const MaxAge = time.Minute

// refreshTimeout bounds each caut call: caut reads provider usage over the
// network.
const refreshTimeout = 20 * time.Second

// UsagePoller holds the caut usage cache read by the TUI quota panel and
// robot quota status. The background polling lifecycle that once filled the
// cache was removed with the caut->CAAM coordinator
// (bd-ws2-wire-or-delete-ykmcz.10), and the [integrations.caut] config
// section was removed with it (bd-ws6-config-truth-ienmd.2); readers refill
// the cache on demand with RefreshIfStale.
type UsagePoller struct {
	cache     *UsageCache
	refreshMu sync.Mutex
}

// RefreshIfStale refills the cache from caut when its data is older than
// maxAge, and leaves it untouched when caut is not installed. Each provider's
// primary rate window becomes its quota percentage, and the overall
// percentage is the most-used provider's. Without it the cache stayed empty:
// nothing wrote it once background polling was removed, so the dashboard and
// --robot-quota-status never showed caut usage.
func (p *UsagePoller) RefreshIfStale(ctx context.Context, maxAge time.Duration) {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	if !p.cache.IsStale(maxAge) {
		return
	}
	client := cautcli.NewClient(cautcli.WithTimeout(refreshTimeout))
	if !client.IsInstalled() {
		return
	}

	providers := cautcli.SupportedProviders()
	payloads := make([]*cautcli.ProviderPayload, len(providers))
	errs := make([]error, len(providers))
	var wg sync.WaitGroup
	for i, provider := range providers {
		wg.Add(1)
		go func(i int, provider string) {
			defer wg.Done()
			payloads[i], errs[i] = client.GetProviderUsage(ctx, provider)
		}(i, provider)
	}
	wg.Wait()

	status := &tools.CautStatus{
		Running:     true,
		Tracking:    true,
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
	}
	var firstErr error
	var details []ProviderDetail
	for i, payload := range payloads {
		if payload == nil {
			// A provider caut has no account for answers with no data; that
			// is not a failure of the read.
			if firstErr == nil && errs[i] != nil && !errors.Is(errs[i], cautcli.ErrNoData) {
				firstErr = errs[i]
			}
			continue
		}
		entry := tools.CautProvider{Name: payload.Provider, Enabled: true}
		if used := payload.UsedPercent(); used != nil {
			entry.HasQuota = true
			entry.QuotaUsed = *used
			if *used > status.QuotaPercent {
				status.QuotaPercent = *used
			}
		}
		status.Providers = append(status.Providers, entry)

		detail := ProviderDetail{
			Provider:         payload.Provider,
			AccountEmail:     payload.GetAccountEmail(),
			HasUsageData:     payload.HasUsageData(),
			RateLimited:      payload.IsRateLimited(95.0),
			ResetDescription: payload.GetResetDescription(),
		}
		if wm := payload.GetWindowMinutes(); wm != nil {
			detail.WindowMinutes = *wm
		}
		if rt := payload.GetResetTime(); rt != nil {
			detail.ResetAt = rt.UTC().Format(time.RFC3339)
		}
		details = append(details, detail)
	}
	status.ProviderCount = len(status.Providers)
	if status.ProviderCount == 0 && firstErr != nil {
		// Record the failed read as this check's result so readers do not
		// re-run every caut call until maxAge passes.
		p.cache.UpdateProviderDetails(nil)
		p.cache.UpdateStatus(nil)
		p.cache.SetError(firstErr)
		return
	}
	p.cache.ClearError()
	p.cache.UpdateProviderDetails(details)
	p.cache.UpdateStatus(status)
}

// NewUsagePoller creates a new usage poller wrapping a fresh cache.
func NewUsagePoller() *UsagePoller {
	return &UsagePoller{
		cache: NewUsageCache(),
	}
}

// GetCache returns the usage cache for reading cached data.
func (p *UsagePoller) GetCache() *UsageCache {
	return p.cache
}

// Global poller instance management

var (
	globalPoller     *UsagePoller
	globalPollerOnce sync.Once
)

// GetGlobalPoller returns the global caut usage poller singleton.
func GetGlobalPoller() *UsagePoller {
	globalPollerOnce.Do(func() {
		globalPoller = NewUsagePoller()
	})
	return globalPoller
}
