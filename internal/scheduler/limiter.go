package scheduler

import (
	"context"
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter for spawn operations.
// It controls the rate at which spawn jobs can be executed to prevent
// resource exhaustion and API rate limits.
type RateLimiter struct {
	mu sync.Mutex

	// rate is tokens per second added to the bucket.
	rate float64

	// capacity is the maximum number of tokens the bucket can hold.
	capacity float64

	// tokens is the current number of available tokens.
	tokens float64

	// lastUpdate is when tokens were last updated.
	lastUpdate time.Time

	// burstAllowed enables burst mode (use all capacity at once).
	burstAllowed bool

	// minInterval is the minimum time between operations (leaky bucket aspect).
	minInterval time.Duration

	// lastOp is when the last operation was allowed.
	lastOp time.Time

	// waiting tracks how many requests are waiting for tokens.
	waiting int

	// stats tracks limiter statistics.
	stats LimiterStats
}

// LimiterConfig configures the rate limiter.
type LimiterConfig struct {
	// Rate is the number of tokens added per second.
	Rate float64 `json:"rate"`

	// Capacity is the maximum number of tokens (burst size).
	Capacity float64 `json:"capacity"`

	// BurstAllowed enables burst mode.
	BurstAllowed bool `json:"burst_allowed"`

	// MinInterval is the minimum time between operations.
	MinInterval time.Duration `json:"min_interval"`
}

// LimiterStats contains rate limiter statistics.
type LimiterStats struct {
	// TotalRequests is the total number of Wait calls.
	TotalRequests int64 `json:"total_requests"`

	// AllowedRequests is requests that were allowed immediately.
	AllowedRequests int64 `json:"allowed_requests"`

	// WaitedRequests is requests that had to wait.
	WaitedRequests int64 `json:"waited_requests"`

	// DeniedRequests is requests denied due to timeout/cancellation.
	DeniedRequests int64 `json:"denied_requests"`

	// TotalWaitTime is the cumulative wait time.
	TotalWaitTime time.Duration `json:"total_wait_time"`

	// MaxWaitTime is the longest wait time recorded.
	MaxWaitTime time.Duration `json:"max_wait_time"`

	// AvgWaitTime is the average wait time.
	AvgWaitTime time.Duration `json:"avg_wait_time"`

	// CurrentTokens is the current token count.
	CurrentTokens float64 `json:"current_tokens"`

	// Waiting is the number of requests currently waiting.
	Waiting int `json:"waiting"`
}

// DefaultLimiterConfig returns sensible default configuration.
func DefaultLimiterConfig() LimiterConfig {
	return LimiterConfig{
		Rate:         2.0,              // 2 spawns per second
		Capacity:     5.0,              // Allow burst of 5
		BurstAllowed: true,             // Allow bursting
		MinInterval:  300 * time.Millisecond, // 300ms minimum between spawns
	}
}

// NewRateLimiter creates a new rate limiter with the given configuration.
func NewRateLimiter(cfg LimiterConfig) *RateLimiter {
	if cfg.Rate <= 0 {
		cfg.Rate = 2.0
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = 5.0
	}

	return &RateLimiter{
		rate:         cfg.Rate,
		capacity:     cfg.Capacity,
		tokens:       cfg.Capacity, // Start with full capacity
		lastUpdate:   time.Now(),
		burstAllowed: cfg.BurstAllowed,
		minInterval:  cfg.MinInterval,
	}
}

// refill adds tokens based on elapsed time.
func (r *RateLimiter) refill() {
	now := time.Now()
	elapsed := now.Sub(r.lastUpdate)
	r.lastUpdate = now

	// Add tokens based on elapsed time
	tokensToAdd := elapsed.Seconds() * r.rate
	r.tokens += tokensToAdd
	if r.tokens > r.capacity {
		r.tokens = r.capacity
	}
}

// Wait blocks until a token is available or context is cancelled.
// Returns nil if a token was acquired, or an error if cancelled/timed out.
func (r *RateLimiter) Wait(ctx context.Context) error {
	r.mu.Lock()
	r.stats.TotalRequests++
	r.waiting++

	startTime := time.Now()

	for {
		r.refill()

		// Check minimum interval
		if r.minInterval > 0 && !r.lastOp.IsZero() {
			elapsed := time.Since(r.lastOp)
			if elapsed < r.minInterval {
				waitTime := r.minInterval - elapsed
				r.mu.Unlock()

				select {
				case <-ctx.Done():
					r.mu.Lock()
					r.waiting--
					r.stats.DeniedRequests++
					r.mu.Unlock()
					return ctx.Err()
				case <-time.After(waitTime):
				}

				r.mu.Lock()
				r.refill()
			}
		}

		// Try to acquire a token
		if r.tokens >= 1 {
			r.tokens--
			r.lastOp = time.Now()
			r.stats.CurrentTokens = r.tokens
			r.waiting--

			waitDuration := time.Since(startTime)
			if waitDuration > 10*time.Millisecond {
				r.stats.WaitedRequests++
				r.stats.TotalWaitTime += waitDuration
				if waitDuration > r.stats.MaxWaitTime {
					r.stats.MaxWaitTime = waitDuration
				}
				if r.stats.WaitedRequests > 0 {
					r.stats.AvgWaitTime = r.stats.TotalWaitTime / time.Duration(r.stats.WaitedRequests)
				}
			} else {
				r.stats.AllowedRequests++
			}

			r.mu.Unlock()
			return nil
		}

		// Calculate wait time for next token
		tokensNeeded := 1 - r.tokens
		waitDuration := time.Duration(tokensNeeded/r.rate*1000) * time.Millisecond
		if waitDuration < time.Millisecond {
			waitDuration = time.Millisecond
		}

		r.mu.Unlock()

		select {
		case <-ctx.Done():
			r.mu.Lock()
			r.waiting--
			r.stats.DeniedRequests++
			r.mu.Unlock()
			return ctx.Err()
		case <-time.After(waitDuration):
		}

		r.mu.Lock()
	}
}

// TryAcquire tries to acquire a token without blocking.
// Returns true if a token was acquired, false otherwise.
func (r *RateLimiter) TryAcquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.refill()
	r.stats.TotalRequests++

	// Check minimum interval
	if r.minInterval > 0 && !r.lastOp.IsZero() {
		if time.Since(r.lastOp) < r.minInterval {
			return false
		}
	}

	if r.tokens >= 1 {
		r.tokens--
		r.lastOp = time.Now()
		r.stats.CurrentTokens = r.tokens
		r.stats.AllowedRequests++
		return true
	}

	return false
}

// TimeUntilNextToken returns the estimated time until a token is available.
func (r *RateLimiter) TimeUntilNextToken() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.refill()

	if r.tokens >= 1 {
		// Check min interval
		if r.minInterval > 0 && !r.lastOp.IsZero() {
			elapsed := time.Since(r.lastOp)
			if elapsed < r.minInterval {
				return r.minInterval - elapsed
			}
		}
		return 0
	}

	tokensNeeded := 1 - r.tokens
	waitTime := time.Duration(tokensNeeded/r.rate*1000) * time.Millisecond

	// Also consider min interval
	if r.minInterval > 0 && !r.lastOp.IsZero() {
		elapsed := time.Since(r.lastOp)
		if elapsed < r.minInterval {
			intervalWait := r.minInterval - elapsed
			if intervalWait > waitTime {
				return intervalWait
			}
		}
	}

	return waitTime
}

// SetRate updates the token refill rate.
func (r *RateLimiter) SetRate(rate float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refill() // Settle current state first
	if rate > 0 {
		r.rate = rate
	}
}

// SetCapacity updates the maximum bucket capacity.
func (r *RateLimiter) SetCapacity(capacity float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if capacity > 0 {
		r.capacity = capacity
		if r.tokens > r.capacity {
			r.tokens = r.capacity
		}
	}
}

// SetMinInterval updates the minimum interval between operations.
func (r *RateLimiter) SetMinInterval(interval time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.minInterval = interval
}

// Stats returns a copy of the current statistics.
func (r *RateLimiter) Stats() LimiterStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refill()
	r.stats.CurrentTokens = r.tokens
	r.stats.Waiting = r.waiting
	return r.stats
}

// Reset resets the limiter to initial state.
func (r *RateLimiter) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = r.capacity
	r.lastUpdate = time.Now()
	r.lastOp = time.Time{}
	r.waiting = 0
	r.stats = LimiterStats{}
}

// AvailableTokens returns the current number of available tokens.
func (r *RateLimiter) AvailableTokens() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refill()
	return r.tokens
}

// Waiting returns the number of requests currently waiting.
func (r *RateLimiter) Waiting() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waiting
}

// PerAgentLimiter provides per-agent-type rate limiting.
type PerAgentLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*RateLimiter
	defaults LimiterConfig
}

// AgentLimiterConfig contains configuration for per-agent limiting.
type AgentLimiterConfig struct {
	// Default is the default limiter config for unknown agent types.
	Default LimiterConfig `json:"default"`

	// PerAgent contains per-agent-type overrides.
	PerAgent map[string]LimiterConfig `json:"per_agent,omitempty"`
}

// DefaultAgentLimiterConfig returns sensible defaults for agent rate limiting.
func DefaultAgentLimiterConfig() AgentLimiterConfig {
	return AgentLimiterConfig{
		Default: DefaultLimiterConfig(),
		PerAgent: map[string]LimiterConfig{
			"cc": {
				Rate:         1.5,              // Claude is more conservative
				Capacity:     3.0,
				BurstAllowed: true,
				MinInterval:  500 * time.Millisecond,
			},
			"cod": {
				Rate:         1.0,              // Codex is more rate-limited
				Capacity:     2.0,
				BurstAllowed: true,
				MinInterval:  800 * time.Millisecond,
			},
			"gmi": {
				Rate:         2.0,              // Gemini allows more
				Capacity:     5.0,
				BurstAllowed: true,
				MinInterval:  400 * time.Millisecond,
			},
		},
	}
}

// NewPerAgentLimiter creates a new per-agent rate limiter.
func NewPerAgentLimiter(cfg AgentLimiterConfig) *PerAgentLimiter {
	pal := &PerAgentLimiter{
		limiters: make(map[string]*RateLimiter),
		defaults: cfg.Default,
	}

	// Pre-create configured agent limiters
	for agent, limiterCfg := range cfg.PerAgent {
		pal.limiters[agent] = NewRateLimiter(limiterCfg)
	}

	return pal
}

// GetLimiter returns the rate limiter for an agent type.
func (p *PerAgentLimiter) GetLimiter(agentType string) *RateLimiter {
	p.mu.RLock()
	limiter, ok := p.limiters[agentType]
	p.mu.RUnlock()

	if ok {
		return limiter
	}

	// Create default limiter for unknown agent type
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, ok := p.limiters[agentType]; ok {
		return limiter
	}

	limiter = NewRateLimiter(p.defaults)
	p.limiters[agentType] = limiter
	return limiter
}

// Wait waits for a token from the agent-specific limiter.
func (p *PerAgentLimiter) Wait(ctx context.Context, agentType string) error {
	return p.GetLimiter(agentType).Wait(ctx)
}

// AllStats returns statistics for all agent limiters.
func (p *PerAgentLimiter) AllStats() map[string]LimiterStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := make(map[string]LimiterStats)
	for agent, limiter := range p.limiters {
		stats[agent] = limiter.Stats()
	}
	return stats
}
