package webhook

// WS6-wire behavior proof (bd-ws6-config-truth-ienmd.1): flipping the
// [retry] / [retry.webhook] knobs changes the observed number of webhook
// delivery attempts made by the manager against a real HTTP server, and the
// backoff-shape globals (max_delay_ms, backoff_factor, jitter) govern the
// computed retry schedule.

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func resetWebhookRetryPolicy() {
	retryPolicyMu.Lock()
	activePolicy = retryPolicy{
		maxRetries:    DefaultMaxRetries,
		baseDelay:     DefaultBaseBackoff,
		maxDelay:      DefaultMaxBackoff,
		backoffFactor: 2.0,
		jitter:        false,
	}
	retryPolicyMu.Unlock()
}

func applyCentralRetryConfig(cfg config.RetryConfig) {
	maxAttempts, initialDelay := cfg.RetryPolicyFor("webhook")
	ApplyRetryPolicy(maxAttempts, initialDelay, cfg.MaxDelayMs, cfg.BackoffFactor, cfg.Jitter)
}

// TestManagerRetryPolicyGovernsAttemptCounts proves the central [retry]
// policy governs webhook delivery retries for webhooks that do not carry
// their own retry settings: observed HTTP attempts flip with
// retry.webhook.max_attempts.
func TestManagerRetryPolicyGovernsAttemptCounts(t *testing.T) {
	defer resetWebhookRetryPolicy()

	cases := []struct {
		name         string
		retryCfg     config.RetryConfig
		wantAttempts int32
	}{
		{
			// Shipping default: Webhook.MaxAttempts=5. The manager counts the
			// first delivery as attempt 1, so max_attempts bounds TOTAL
			// attempts (historical DefaultMaxRetries=5 behaved identically).
			// Delays shrunk for test speed only (counts are the assertion).
			name: "default_webhook_policy_five_attempts",
			retryCfg: config.RetryConfig{
				MaxAttempts:    3,
				InitialDelayMs: 1,
				MaxDelayMs:     5,
				BackoffFactor:  2.0,
				Webhook:        config.RetryOverride{MaxAttempts: 5},
			},
			wantAttempts: 5,
		},
		{
			name: "custom_webhook_policy_two_attempts",
			retryCfg: config.RetryConfig{
				MaxAttempts:    3,
				InitialDelayMs: 1,
				MaxDelayMs:     5,
				BackoffFactor:  2.0,
				Webhook:        config.RetryOverride{MaxAttempts: 2, InitialDelayMs: 1},
			},
			wantAttempts: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetWebhookRetryPolicy()
			applyCentralRetryConfig(tc.retryCfg)

			var attempts atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(http.StatusInternalServerError) // retryable
			}))
			defer ts.Close()

			m := NewManager(ManagerConfig{QueueSize: 10, WorkerCount: 2})
			// No per-webhook MaxRetries: Register fills the retry policy from
			// the central [retry] defaults — the wiring under test.
			if err := m.Register(WebhookConfig{
				ID:      "ws6-retry-policy",
				URL:     ts.URL,
				Enabled: true,
				Retry:   RetryConfig{Enabled: true},
			}); err != nil {
				t.Fatalf("registration failed: %v", err)
			}
			if err := m.Start(); err != nil {
				t.Fatalf("start failed: %v", err)
			}
			defer m.Stop()

			if err := m.Dispatch(Event{Type: "ws6.retry"}); err != nil {
				t.Fatalf("dispatch failed: %v", err)
			}

			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if attempts.Load() >= tc.wantAttempts {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			// Settle window: prove the count STOPS at the policy bound.
			time.Sleep(150 * time.Millisecond)
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Fatalf("observed %d attempts, want %d (policy must govern retry count)", got, tc.wantAttempts)
			}
		})
	}
}

// TestNextRetryDelayGovernedByBackoffGlobals proves retry.backoff_factor and
// retry.max_delay_ms shape the retry schedule.
func TestNextRetryDelayGovernedByBackoffGlobals(t *testing.T) {
	defer resetWebhookRetryPolicy()

	resetWebhookRetryPolicy()
	applyCentralRetryConfig(config.RetryConfig{
		MaxAttempts:    3,
		InitialDelayMs: 100,
		MaxDelayMs:     30000,
		BackoffFactor:  2.0,
	})
	if got := nextRetryDelay(3, 0, 0); got != 400*time.Millisecond {
		t.Errorf("factor 2.0: attempt-3 delay = %v, want 400ms", got)
	}

	applyCentralRetryConfig(config.RetryConfig{
		MaxAttempts:    3,
		InitialDelayMs: 100,
		MaxDelayMs:     30000,
		BackoffFactor:  3.0,
	})
	if got := nextRetryDelay(3, 0, 0); got != 900*time.Millisecond {
		t.Errorf("factor 3.0: attempt-3 delay = %v, want 900ms", got)
	}

	applyCentralRetryConfig(config.RetryConfig{
		MaxAttempts:    3,
		InitialDelayMs: 100,
		MaxDelayMs:     250, // cap below the un-capped 400ms schedule
		BackoffFactor:  2.0,
	})
	if got := nextRetryDelay(3, 0, 0); got != 250*time.Millisecond {
		t.Errorf("max_delay cap: attempt-3 delay = %v, want 250ms", got)
	}
}

// TestNextRetryDelayJitterKnob proves retry.jitter randomizes delays only
// when enabled; the default (false) keeps the deterministic historical
// schedule.
func TestNextRetryDelayJitterKnob(t *testing.T) {
	defer resetWebhookRetryPolicy()

	resetWebhookRetryPolicy()
	base := config.RetryConfig{
		MaxAttempts:    3,
		InitialDelayMs: 1000,
		MaxDelayMs:     30000,
		BackoffFactor:  2.0,
	}
	applyCentralRetryConfig(base)
	for i := 0; i < 5; i++ {
		if got := nextRetryDelay(2, 0, 0); got != 2*time.Second {
			t.Fatalf("jitter off: delay = %v, want deterministic 2s", got)
		}
	}

	base.Jitter = true
	applyCentralRetryConfig(base)
	varied := false
	for i := 0; i < 32; i++ {
		got := nextRetryDelay(2, 0, 0)
		if got < time.Second || got > 2*time.Second {
			t.Fatalf("jitter on: delay %v outside [half, full] envelope", got)
		}
		if got != 2*time.Second {
			varied = true
		}
	}
	if !varied {
		t.Error("jitter on: delays never varied across 32 samples")
	}
}

// TestWebhookRetryDefaultsMatchHistoricalBehavior pins the invariant that the
// shipping default config resolves to the pre-wiring constants (5 retries,
// 1s base, 30s cap, doubling, no jitter).
func TestWebhookRetryDefaultsMatchHistoricalBehavior(t *testing.T) {
	defer resetWebhookRetryPolicy()
	resetWebhookRetryPolicy()

	applyCentralRetryConfig(config.DefaultRetryConfig())
	p := currentRetryPolicy()
	if p.maxRetries != 5 {
		t.Errorf("default webhook max retries = %d, want historical 5", p.maxRetries)
	}
	if p.baseDelay != time.Second {
		t.Errorf("default webhook base delay = %v, want historical 1s", p.baseDelay)
	}
	if p.maxDelay != 30*time.Second {
		t.Errorf("default webhook max delay = %v, want historical 30s", p.maxDelay)
	}
	if p.backoffFactor != 2.0 {
		t.Errorf("default webhook backoff factor = %v, want historical 2.0", p.backoffFactor)
	}
	if p.jitter {
		t.Error("default webhook jitter = true, want historical false")
	}
}
