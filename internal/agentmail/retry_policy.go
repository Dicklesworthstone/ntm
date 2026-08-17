package agentmail

// WS6-wire (bd-ws6-config-truth-ienmd.1): the Agent Mail MCP busy-retry loop
// (callToolWithBusyRetry) is governed by the central [retry] policy via
// config.RetryConfig.RetryPolicyFor("agent_mail") instead of hardcoded
// constants. ApplyRetryPolicy is invoked once per process after config load
// (internal/cli/root.go) with that policy; the compiled-in defaults preserve
// the historical behavior exactly (3 busy retries after the first call, 500ms
// initial backoff, doubling).

import (
	"log/slog"
	"sync"
	"time"
)

const (
	defaultBusyMaxRetries     = 3
	defaultBusyInitialBackoff = 500 * time.Millisecond
)

var (
	busyRetryMu             sync.RWMutex
	busyRetryMaxRetries     = defaultBusyMaxRetries
	busyRetryInitialBackoff = defaultBusyInitialBackoff
)

// ApplyRetryPolicy configures the busy-retry loop for Agent Mail tool calls.
// maxAttempts is the number of retries after the first call (matching the
// loop's historical maxRetries semantics); initialDelayMs is the first
// backoff, which doubles per retry. Call it with
// cfg.Retry.RetryPolicyFor("agent_mail"). Non-positive values keep the
// compiled-in defaults.
func ApplyRetryPolicy(maxAttempts int, initialDelayMs int) {
	busyRetryMu.Lock()
	defer busyRetryMu.Unlock()
	if maxAttempts > 0 {
		busyRetryMaxRetries = maxAttempts
	}
	if initialDelayMs > 0 {
		busyRetryInitialBackoff = time.Duration(initialDelayMs) * time.Millisecond
	}
	slog.Debug("agentmail busy-retry policy applied",
		"max_retries", busyRetryMaxRetries,
		"initial_backoff", busyRetryInitialBackoff)
}

// busyRetryPolicy returns the active busy-retry policy.
func busyRetryPolicy() (maxRetries int, initialBackoff time.Duration) {
	busyRetryMu.RLock()
	defer busyRetryMu.RUnlock()
	return busyRetryMaxRetries, busyRetryInitialBackoff
}

// busyMaxRetries returns the configured busy-retry budget for tool calls.
func busyMaxRetries() int {
	max, _ := busyRetryPolicy()
	return max
}

// G2 liveness note: this package cannot import internal/config (cycle via
// internal/watcher → agentmail), so the RegisterReader claims for
// retry.agent_mail.* are made in internal/cli/policy_wiring.go with
// ApplyRetryPolicy — this function — as the claimed reader reference.
