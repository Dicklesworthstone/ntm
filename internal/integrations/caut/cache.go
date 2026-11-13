// Package caut provides integration with the caut (Cloud API Usage Tracker) tool.
// It includes background polling for usage data and caching for dashboard display.
package caut

import (
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// ProviderDetail captures rate-window and account metadata parsed from caut payloads.
type ProviderDetail struct {
	Provider         string `json:"provider"`
	AccountEmail     string `json:"account_email,omitempty"`
	WindowMinutes    int    `json:"window_minutes,omitempty"`
	HasUsageData     bool   `json:"has_usage_data"`
	RateLimited      bool   `json:"rate_limited,omitempty"`
	ResetAt          string `json:"reset_at,omitempty"`
	ResetDescription string `json:"reset_description,omitempty"`
}

// UsageCache stores cached usage data for quick dashboard access.
// It is thread-safe for concurrent read/write access.
type UsageCache struct {
	mu sync.RWMutex

	// Status is the most recent caut status
	status *tools.CautStatus

	// Usage is per-provider usage data
	usage map[string]*tools.CautUsage

	// details stores per-provider rate-window and identity details
	details map[string]*ProviderDetail

	// LastUpdated is when the cache was last refreshed
	lastUpdated time.Time

	// UpdateCount tracks how many times the cache has been updated
	updateCount int64

	// Errors tracks recent errors for debugging
	lastError     error
	lastErrorTime time.Time
}

// NewUsageCache creates a new usage cache.
func NewUsageCache() *UsageCache {
	return &UsageCache{
		usage:   make(map[string]*tools.CautUsage),
		details: make(map[string]*ProviderDetail),
	}
}

// UpdateStatus updates the cached status.
func (c *UsageCache) UpdateStatus(status *tools.CautStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Copy status to prevent external mutation
	if status != nil {
		statusCopy := *status
		c.status = &statusCopy
	} else {
		c.status = nil
	}
	c.lastUpdated = time.Now()
	c.updateCount++
}

// UpdateUsage updates usage data for a specific provider.
func (c *UsageCache) UpdateUsage(provider string, usage *tools.CautUsage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Copy usage to prevent external mutation
	if usage != nil {
		usageCopy := *usage
		c.usage[provider] = &usageCopy
	}
	c.lastUpdated = time.Now()
	c.updateCount++
}

// UpdateAllUsage updates usage data for all providers.
func (c *UsageCache) UpdateAllUsage(usages []tools.CautUsage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Clear existing and repopulate
	c.usage = make(map[string]*tools.CautUsage)
	for i := range usages {
		usageCopy := usages[i]
		c.usage[usageCopy.Provider] = &usageCopy
	}
	c.lastUpdated = time.Now()
	c.updateCount++
}

// SetError records an error that occurred during polling.
func (c *UsageCache) SetError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastError = err
	c.lastErrorTime = time.Now()
}

// ClearError clears the last recorded error.
func (c *UsageCache) ClearError() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastError = nil
	c.lastErrorTime = time.Time{}
}

// GetStatus returns the cached status.
func (c *UsageCache) GetStatus() *tools.CautStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.status == nil {
		return nil
	}

	// Return a copy to prevent external mutation
	statusCopy := *c.status
	return &statusCopy
}

// GetUsage returns usage data for a specific provider.
func (c *UsageCache) GetUsage(provider string) *tools.CautUsage {
	c.mu.RLock()
	defer c.mu.RUnlock()

	usage, ok := c.usage[provider]
	if !ok {
		return nil
	}

	// Return a copy to prevent external mutation
	usageCopy := *usage
	return &usageCopy
}

// GetAllUsage returns usage data for all cached providers.
func (c *UsageCache) GetAllUsage() []tools.CautUsage {
	c.mu.RLock()
	defer c.mu.RUnlock()

	usages := make([]tools.CautUsage, 0, len(c.usage))
	providers := make([]string, 0, len(c.usage))
	for provider := range c.usage {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		usages = append(usages, *c.usage[provider])
	}
	return usages
}

// GetUpdateCount returns how many times the cache has been updated.
func (c *UsageCache) GetUpdateCount() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.updateCount
}

// GetLastError returns when the most recent error occurred and the error itself.
func (c *UsageCache) GetLastError() (time.Time, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErrorTime, c.lastError
}

// IsStale returns true if the cache is older than the given duration.
func (c *UsageCache) IsStale(maxAge time.Duration) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.lastUpdated.IsZero() {
		return true
	}
	return time.Since(c.lastUpdated) > maxAge
}

// UpdateProviderDetails stores per-provider rate-window and account metadata.
func (c *UsageCache) UpdateProviderDetails(details []ProviderDetail) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.details = make(map[string]*ProviderDetail, len(details))
	for i := range details {
		detailCopy := details[i]
		c.details[detailCopy.Provider] = &detailCopy
	}
}

// GetProviderDetails returns all cached per-provider details sorted by provider name.
func (c *UsageCache) GetProviderDetails() []ProviderDetail {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.details) == 0 {
		return nil
	}
	providers := make([]string, 0, len(c.details))
	for provider := range c.details {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	out := make([]ProviderDetail, 0, len(providers))
	for _, provider := range providers {
		out = append(out, *c.details[provider])
	}
	return out
}

// CacheSnapshot represents a point-in-time snapshot of the cache.
type CacheSnapshot struct {
	Status          *tools.CautStatus `json:"status,omitempty"`
	Usage           []tools.CautUsage `json:"usage,omitempty"`
	ProviderDetails []ProviderDetail  `json:"provider_details,omitempty"`
	LastUpdated     time.Time         `json:"last_updated"`
	UpdateCount     int64             `json:"update_count"`
	HasError        bool              `json:"has_error"`
	ErrorMessage    string            `json:"error_message,omitempty"`
	ErrorTime       *time.Time        `json:"error_time,omitempty"`
}

// Snapshot returns a complete snapshot of the cache state.
func (c *UsageCache) Snapshot() CacheSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	snapshot := CacheSnapshot{
		LastUpdated: c.lastUpdated,
		UpdateCount: c.updateCount,
		Usage:       make([]tools.CautUsage, 0, len(c.usage)),
	}

	if c.status != nil {
		statusCopy := *c.status
		snapshot.Status = &statusCopy
	}

	providers := make([]string, 0, len(c.usage))
	for provider := range c.usage {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		snapshot.Usage = append(snapshot.Usage, *c.usage[provider])
	}

	if len(c.details) > 0 {
		detailKeys := make([]string, 0, len(c.details))
		for provider := range c.details {
			detailKeys = append(detailKeys, provider)
		}
		sort.Strings(detailKeys)
		snapshot.ProviderDetails = make([]ProviderDetail, 0, len(detailKeys))
		for _, provider := range detailKeys {
			snapshot.ProviderDetails = append(snapshot.ProviderDetails, *c.details[provider])
		}
	}

	if c.lastError != nil {
		snapshot.HasError = true
		snapshot.ErrorMessage = c.lastError.Error()
		errorTime := c.lastErrorTime
		snapshot.ErrorTime = &errorTime
	}

	return snapshot
}

// Clear resets the cache to empty state.
func (c *UsageCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.status = nil
	c.usage = make(map[string]*tools.CautUsage)
	c.details = make(map[string]*ProviderDetail)
	c.lastUpdated = time.Time{}
	c.updateCount = 0
	c.lastError = nil
	c.lastErrorTime = time.Time{}
}
