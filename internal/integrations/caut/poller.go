package caut

import (
	"sync"
)

// UsagePoller holds the caut usage cache read by the TUI quota panel and
// robot quota status. The background polling lifecycle that once filled the
// cache was removed with the caut->CAAM coordinator
// (bd-ws2-wire-or-delete-ykmcz.10), and the [integrations.caut] config
// section was removed with it (bd-ws6-config-truth-ienmd.2); the cache is
// populated only by callers that write to it directly.
type UsagePoller struct {
	cache *UsageCache
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
