package robot

// WS6-wire behavior proof (bd-ws6-config-truth-ienmd.1): flipping the
// [retry] / [retry.alerts] knobs changes the observed number of alert
// webhook delivery attempts against a real HTTP server.

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/notify"
)

func resetAlertRetryPolicy() {
	notify.ApplyAlertRetryPolicy(3, 1000)
}

// TestAlertRetryPolicyDefaultsMatchHistoricalBehavior pins the invariant that
// the shipping default config preserves the retry budget: 3 retries after the
// first attempt with a 1s base backoff, and flipping [retry.alerts] changes
// observed attempts against a real HTTP webhook server.
func TestAlertRetryPolicyDefaultsMatchHistoricalBehavior(t *testing.T) {
	defer resetAlertRetryPolicy()
	resetAlertRetryPolicy()

	def := config.DefaultRetryConfig()
	notify.ApplyAlertRetryPolicy(def.RetryPolicyFor("alerts"))
	maxRetries, base := notify.AlertRetryPolicy()
	if maxRetries != 3 {
		t.Errorf("default alerts max retries = %d, want historical 3", maxRetries)
	}
	if base != time.Second {
		t.Errorf("default alerts base delay = %v, want historical 1s", base)
	}

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := config.DefaultRetryConfig()
	cfg.Alerts.MaxAttempts = 2
	cfg.Alerts.InitialDelayMs = 1
	notify.ApplyAlertRetryPolicy(cfg.RetryPolicyFor("alerts"))

	notifier := notify.New(notify.Config{
		Enabled: true,
		Events:  []string{string(notify.EventAgentError)},
		Webhook: notify.WebhookConfig{
			Enabled: true,
			URL:     srv.URL,
		},
	})
	if err := notifier.Notify(notify.Event{
		Type:      notify.EventAgentError,
		Timestamp: time.Now().UTC(),
		Session:   "retry-test",
		Message:   "transient webhook outage",
	}); err != nil {
		t.Fatalf("Notify failed after retryable 503s: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("observed webhook attempts = %d, want 3 (1 initial + 2 retries)", got)
	}

	// Non-retryable 400 must stop immediately on the first attempt.
	attempts.Store(0)
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer badSrv.Close()

	badNotifier := notify.New(notify.Config{
		Enabled: true,
		Events:  []string{string(notify.EventAgentError)},
		Webhook: notify.WebhookConfig{
			Enabled: true,
			URL:     badSrv.URL,
		},
	})
	if err := badNotifier.Notify(notify.Event{
		Type:      notify.EventAgentError,
		Timestamp: time.Now().UTC(),
		Session:   "retry-test",
		Message:   "bad payload",
	}); err == nil {
		t.Fatal("Notify succeeded on HTTP 400, want error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("observed HTTP 400 attempts = %d, want 1 (no retry on 4xx)", got)
	}
}
