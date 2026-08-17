package webhook

// WS6-wire (bd-ws6-config-truth-ienmd.1): webhook event delivery retries are
// governed by the central [retry] policy (globals + [retry.webhook] override)
// instead of compiled-in constants. ApplyRetryPolicy is invoked once per
// process after config load (internal/cli/root.go) with
// cfg.Retry.RetryPolicyFor("webhook") plus the global backoff shape knobs;
// the compiled-in defaults preserve historical behavior exactly (5 retries,
// 1s base doubling to a 30s cap, no jitter).

import (
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

type retryPolicy struct {
	maxRetries    int
	baseDelay     time.Duration
	maxDelay      time.Duration
	backoffFactor float64
	jitter        bool
}

var (
	retryPolicyMu sync.RWMutex
	activePolicy  = retryPolicy{
		maxRetries:    DefaultMaxRetries,
		baseDelay:     DefaultBaseBackoff,
		maxDelay:      DefaultMaxBackoff,
		backoffFactor: 2.0,
		jitter:        false,
	}
)

// ApplyRetryPolicy configures the delivery retry policy defaults for webhook
// managers. maxAttempts and initialDelayMs come from
// cfg.Retry.RetryPolicyFor("webhook"); maxDelayMs, backoffFactor, and jitter
// are the [retry] globals. Non-positive values keep the compiled-in defaults.
// Per-webhook [[webhooks]] retry settings still take precedence — this policy
// supplies the defaults used when a webhook does not set its own.
func ApplyRetryPolicy(maxAttempts, initialDelayMs, maxDelayMs int, backoffFactor float64, jitter bool) {
	retryPolicyMu.Lock()
	defer retryPolicyMu.Unlock()
	if maxAttempts > 0 {
		activePolicy.maxRetries = maxAttempts
	}
	if initialDelayMs > 0 {
		activePolicy.baseDelay = time.Duration(initialDelayMs) * time.Millisecond
	}
	if maxDelayMs > 0 {
		activePolicy.maxDelay = time.Duration(maxDelayMs) * time.Millisecond
	}
	if backoffFactor > 1 {
		activePolicy.backoffFactor = backoffFactor
	}
	activePolicy.jitter = jitter
	slog.Debug("webhook retry policy applied",
		"max_retries", activePolicy.maxRetries,
		"base_delay", activePolicy.baseDelay,
		"max_delay", activePolicy.maxDelay,
		"backoff_factor", activePolicy.backoffFactor,
		"jitter", activePolicy.jitter)
}

func currentRetryPolicy() retryPolicy {
	retryPolicyMu.RLock()
	defer retryPolicyMu.RUnlock()
	return activePolicy
}

// nextRetryDelay computes the backoff before retry number attempt (1-based)
// under the active policy, honoring per-webhook base/max overrides when set.
func nextRetryDelay(attempt int, base, max time.Duration) time.Duration {
	p := currentRetryPolicy()
	if base <= 0 {
		base = p.baseDelay
	}
	if max <= 0 {
		max = p.maxDelay
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay = time.Duration(float64(delay) * p.backoffFactor)
		if delay >= max {
			delay = max
			break
		}
	}
	if delay > max {
		delay = max
	}
	if p.jitter && delay > 0 {
		// Full jitter on the upper half: delay/2 + rand(delay/2), keeping the
		// expected delay close to the deterministic schedule.
		half := delay / 2
		delay = half + time.Duration(rand.Int63n(int64(half)+1))
	}
	return delay
}

func init() {
	// G2 config-key liveness claims: this package reads the [retry] globals
	// and the [retry.webhook] override via ApplyRetryPolicy at startup.
	config.RegisterReader("retry.max_attempts", ApplyRetryPolicy)
	config.RegisterReader("retry.initial_delay_ms", ApplyRetryPolicy)
	config.RegisterReader("retry.max_delay_ms", ApplyRetryPolicy)
	config.RegisterReader("retry.backoff_factor", ApplyRetryPolicy)
	config.RegisterReader("retry.jitter", ApplyRetryPolicy)
	config.RegisterReader("retry.webhook.max_attempts", ApplyRetryPolicy)
	config.RegisterReader("retry.webhook.initial_delay_ms", ApplyRetryPolicy)
}
