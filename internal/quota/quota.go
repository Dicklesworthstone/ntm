// Package quota provides real-time quota tracking for AI providers
// by parsing CLI command outputs (e.g., `claude /usage`).
package quota

import (
	"context"
	"sync"
	"time"
)

// Provider represents an AI provider type
type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
	ProviderGemini Provider = "gemini"
)

// QuotaInfo represents current quota state for an account
type QuotaInfo struct {
	Provider     Provider  `json:"provider"`
	PaneIndex    int       `json:"pane_index,omitempty"`    // Pane index for context
	AccountID    string    `json:"account_id,omitempty"`    // email or unique identifier
	SessionUsage float64   `json:"session_usage,omitempty"` // 0-100 percentage
	PeriodUsage  float64   `json:"period_usage,omitempty"`  // 0-100 (5-hour rolling window)
	WeeklyUsage  float64   `json:"weekly_usage,omitempty"`  // 0-100 percentage
	SonnetUsage  float64   `json:"sonnet_usage,omitempty"`  // 0-100 (Claude sonnet-specific)
	ResetTime    time.Time `json:"reset_time,omitempty"`    // When the period resets
	ResetString  string    `json:"reset_string,omitempty"`  // Raw reset string for display
	IsLimited    bool      `json:"is_limited"`              // Currently rate limited
	Organization string    `json:"organization,omitempty"`  // Account organization
	LoginMethod  string    `json:"login_method,omitempty"`  // OAuth, API key, etc.
	FetchedAt    time.Time `json:"fetched_at"`
	RawOutput    string    `json:"raw_output,omitempty"` // For debugging
	Error        string    `json:"error,omitempty"`      // If fetch failed
}

// IsStale returns true if the quota info is older than the given duration
func (q *QuotaInfo) IsStale(maxAge time.Duration) bool {
	if q == nil {
		return true
	}
	return time.Since(q.FetchedAt) > maxAge
}

// IsHealthy returns true if quota usage is within safe limits
func (q *QuotaInfo) IsHealthy() bool {
	if q == nil {
		return false
	}
	if q.IsLimited {
		return false
	}
	// Consider unhealthy if any quota exceeds 90%
	return q.SessionUsage < 90 && q.WeeklyUsage < 90 && q.PeriodUsage < 90
}

// HighestUsage returns the highest usage percentage across all quota types
func (q *QuotaInfo) HighestUsage() float64 {
	if q == nil {
		return 100 // Assume worst case
	}
	max := q.SessionUsage
	if q.WeeklyUsage > max {
		max = q.WeeklyUsage
	}
	if q.PeriodUsage > max {
		max = q.PeriodUsage
	}
	if q.SonnetUsage > max {
		max = q.SonnetUsage
	}
	return max
}

// cachedQuota holds quota info with expiry tracking
type cachedQuota struct {
	info      *QuotaInfo
	expiresAt time.Time
}

// Tracker manages quota queries and caching for all panes
type Tracker struct {
	mu           sync.RWMutex
	cache        map[string]*cachedQuota // keyed by paneID
	cacheTTL     time.Duration
	pollInterval time.Duration
	pollers      map[string]context.CancelFunc // active pollers by paneID
	fetcher      Fetcher                       // pluggable fetcher for testing
}

// Fetcher interface for quota fetching (allows mocking in tests)
type Fetcher interface {
	// FetchQuota fetches quota info for a pane running a specific provider
	FetchQuota(ctx context.Context, paneID string, provider Provider) (*QuotaInfo, error)
}

// TrackerOption configures the Tracker
type TrackerOption func(*Tracker)

// WithCacheTTL sets the cache TTL
func WithCacheTTL(ttl time.Duration) TrackerOption {
	return func(t *Tracker) {
		t.cacheTTL = ttl
	}
}

// MinPollInterval is the minimum allowed poll interval to prevent ticker panics.
// time.NewTicker requires a positive duration.
const MinPollInterval = 100 * time.Millisecond

// WithPollInterval sets the polling interval
func WithPollInterval(interval time.Duration) TrackerOption {
	return func(t *Tracker) {
		if interval >= MinPollInterval {
			t.pollInterval = interval
		}
		// If interval is too small, keep the default (2 minutes)
	}
}

// WithFetcher sets a custom fetcher (for testing)
func WithFetcher(f Fetcher) TrackerOption {
	return func(t *Tracker) {
		t.fetcher = f
	}
}

// NewTracker creates a new quota tracker
func NewTracker(opts ...TrackerOption) *Tracker {
	t := &Tracker{
		cache:        make(map[string]*cachedQuota),
		cacheTTL:     5 * time.Minute, // Default 5 min cache
		pollInterval: 2 * time.Minute, // Default poll every 2 min
		pollers:      make(map[string]context.CancelFunc),
	}

	for _, opt := range opts {
		opt(t)
	}

	// Use default PTY fetcher if none provided
	if t.fetcher == nil {
		t.fetcher = &PTYFetcher{}
	}

	return t
}

// GetQuota retrieves quota info for a pane, using cache if fresh
func (t *Tracker) GetQuota(paneID string) *QuotaInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()

	cached, ok := t.cache[paneID]
	if !ok {
		return nil
	}

	if time.Now().After(cached.expiresAt) {
		return nil // Expired
	}

	return cached.info
}

// QueryQuota fetches fresh quota info for a pane, bypassing cache
func (t *Tracker) QueryQuota(ctx context.Context, paneID string, provider Provider) (*QuotaInfo, error) {
	info, err := t.fetcher.FetchQuota(ctx, paneID, provider)
	if err != nil {
		return nil, err
	}

	t.updateCache(paneID, info)
	return info, nil
}

// updateCache stores quota info in the cache
func (t *Tracker) updateCache(paneID string, info *QuotaInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cache[paneID] = &cachedQuota{
		info:      info,
		expiresAt: time.Now().Add(t.cacheTTL),
	}
}

// StartPolling begins continuous quota polling for a pane
func (t *Tracker) StartPolling(ctx context.Context, paneID string, provider Provider) {
	t.mu.Lock()

	// Cancel existing poller if any
	if cancel, ok := t.pollers[paneID]; ok {
		cancel()
	}

	pollCtx, cancel := context.WithCancel(ctx)
	t.pollers[paneID] = cancel
	t.mu.Unlock()

	go t.pollLoop(pollCtx, paneID, provider)
}

// StopPolling stops polling for a pane
func (t *Tracker) StopPolling(paneID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if cancel, ok := t.pollers[paneID]; ok {
		cancel()
		delete(t.pollers, paneID)
	}
}

// StopAllPolling stops all active pollers
func (t *Tracker) StopAllPolling() {
	t.mu.Lock()
	defer t.mu.Unlock()

	for paneID, cancel := range t.pollers {
		cancel()
		delete(t.pollers, paneID)
	}
}

// pollLoop continuously polls quota at the configured interval
func (t *Tracker) pollLoop(ctx context.Context, paneID string, provider Provider) {
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	// Initial fetch
	_, _ = t.QueryQuota(ctx, paneID, provider)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = t.QueryQuota(ctx, paneID, provider)
		}
	}
}

// GetAllQuotas returns all cached quota info
func (t *Tracker) GetAllQuotas() map[string]*QuotaInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]*QuotaInfo)
	now := time.Now()

	for paneID, cached := range t.cache {
		if !now.After(cached.expiresAt) {
			result[paneID] = cached.info
		}
	}

	return result
}

// ClearCache removes all cached quota info
func (t *Tracker) ClearCache() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cache = make(map[string]*cachedQuota)
}

// InvalidatePane removes cached quota for a specific pane
func (t *Tracker) InvalidatePane(paneID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.cache, paneID)
}
