package robot

// WS6-wire behavior proof (bd-ws6-config-truth-ienmd.1): flipping the
// [retry] / [retry.alerts] knobs changes the observed number of alert
// webhook delivery attempts against a real HTTP server.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func resetAlertRetryPolicy() {
	alertRetryMu.Lock()
	alertRetryMaxRetries = 3
	alertRetryInitialDelay = time.Second
	alertRetryMu.Unlock()
}

// TestAlertWebhookRetryPolicyGovernsAttemptCounts proves the [retry] policy
// (globals plus [retry.alerts] override) governs the alert delivery retry
// loop: observed HTTP attempts flip with the configured max_attempts.
func TestAlertWebhookRetryPolicyGovernsAttemptCounts(t *testing.T) {
	defer resetAlertRetryPolicy()

	cases := []struct {
		name         string
		retryCfg     config.RetryConfig
		wantAttempts int64
	}{
		{
			// Globals govern alerts when [retry.alerts] is empty (the shipping
			// default): max_attempts=3 -> 1 call + 3 retries = 4 attempts.
			// initial_delay_ms shrunk for test speed only.
			name:         "global_default_four_attempts",
			retryCfg:     config.RetryConfig{MaxAttempts: 3, InitialDelayMs: 1},
			wantAttempts: 4,
		},
		{
			name: "alerts_override_one_retry_two_attempts",
			retryCfg: config.RetryConfig{
				MaxAttempts:    3,
				InitialDelayMs: 1,
				Alerts:         config.RetryOverride{MaxAttempts: 1},
			},
			wantAttempts: 2,
		},
		{
			name: "alerts_override_five_retries_six_attempts",
			retryCfg: config.RetryConfig{
				MaxAttempts:    3,
				InitialDelayMs: 1,
				Alerts:         config.RetryOverride{MaxAttempts: 5, InitialDelayMs: 1},
			},
			wantAttempts: 6,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAlertRetryPolicy()
			ApplyAlertRetryPolicy(tc.retryCfg.RetryPolicyFor("alerts"))

			var attempts atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(http.StatusInternalServerError) // retryable
			}))
			defer server.Close()

			ch := NewWebhookChannel(WebhookConfig{URL: server.URL})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := ch.Send(ctx, &Alert{Type: AlertUnhealthy, Message: "ws6 wiring test"}); err == nil {
				t.Fatal("expected delivery failure from always-500 server")
			}
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Fatalf("observed %d attempts, want %d (policy must govern retry count)", got, tc.wantAttempts)
			}
		})
	}
}

// TestAlertRetryPolicyDefaultsMatchHistoricalBehavior pins the invariant that
// the shipping default config preserves the pre-wiring loop: 3 retries after
// the first attempt with a 1s base backoff.
func TestAlertRetryPolicyDefaultsMatchHistoricalBehavior(t *testing.T) {
	defer resetAlertRetryPolicy()
	resetAlertRetryPolicy()

	def := config.DefaultRetryConfig()
	ApplyAlertRetryPolicy(def.RetryPolicyFor("alerts"))
	alertRetryMu.RLock()
	maxRetries, base := alertRetryMaxRetries, alertRetryInitialDelay
	alertRetryMu.RUnlock()
	if maxRetries != 3 {
		t.Errorf("default alerts max retries = %d, want historical 3", maxRetries)
	}
	if base != time.Second {
		t.Errorf("default alerts base delay = %v, want historical 1s", base)
	}
}
