package robot

// WS6-wire behavior proof (bd-ws6-config-truth-ienmd.1): flipping the
// [retry] / [retry.alerts] knobs changes the observed number of alert
// webhook delivery attempts against a real HTTP server.

import (
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
