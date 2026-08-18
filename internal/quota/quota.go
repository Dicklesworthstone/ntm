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

// HealthState describes the trustworthiness of a quota reading.
type HealthState string

const (
	// HealthUnknown means there is no trustworthy reading: the info is
	// missing or the fetch recorded an error, so the (possibly all-zero)
	// usage numbers cannot be trusted. Absence of data is never evidence
	// of health.
	HealthUnknown HealthState = "unknown"
	// HealthUnhealthy means a successful reading shows the account rate
	// limited or at/over the safe usage threshold.
	HealthUnhealthy HealthState = "unhealthy"
	// HealthHealthy means a successful reading shows usage within safe limits.
	HealthHealthy HealthState = "healthy"
)

// Health classifies the quota reading. A failed fetch (Error set) or nil
// info yields HealthUnknown, never HealthHealthy — a fetch failure leaves
// usage fields at zero, and those zeros must not read as good news.
func (q *QuotaInfo) Health() HealthState {
	if q == nil || q.Error != "" {
		return HealthUnknown
	}
	if q.IsLimited {
		return HealthUnhealthy
	}
	// Consider unhealthy if any quota reaches 90%
	if q.SessionUsage >= 90 || q.WeeklyUsage >= 90 || q.PeriodUsage >= 90 {
		return HealthUnhealthy
	}
	return HealthHealthy
}

// IsHealthy returns true only for a successful reading with quota usage
// within safe limits. A failed fetch reads HealthUnknown and is therefore
// not healthy; a successful fetch at 0% usage is healthy.
func (q *QuotaInfo) IsHealthy() bool {
	return q.Health() == HealthHealthy
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
	pollers      map[string]*pollerHandle // active pollers by paneID
	fetcher      Fetcher                  // pluggable fetcher for testing
}

type pollerHandle struct {
	cancel context.CancelFunc
}

// Fetcher interface for quota fetching (allows mocking in tests)
type Fetcher interface {
	// FetchQuota fetches quota info for a pane running a specific provider
	FetchQuota(ctx context.Context, paneID string, provider Provider) (*QuotaInfo, error)
}

// TrackerOption configures the Tracker
type TrackerOption func(*Tracker)

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

// NewTracker creates a new quota tracker
func NewTracker(opts ...TrackerOption) *Tracker {
	t := &Tracker{
		cache:        make(map[string]*cachedQuota),
		cacheTTL:     5 * time.Minute, // Default 5 min cache
		pollInterval: 2 * time.Minute, // Default poll every 2 min
		pollers:      make(map[string]*pollerHandle),
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

// ClearCache removes all cached quota info
func (t *Tracker) ClearCache() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cache = make(map[string]*cachedQuota)
}
