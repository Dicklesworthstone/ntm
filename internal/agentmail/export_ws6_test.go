package agentmail

// Test-only exports for the WS6 retry-policy behavior tests, which live in
// package agentmail_test because internal/agentmail cannot import
// internal/config directly (import cycle via internal/watcher).

import "time"

// ResetBusyRetryPolicyForTest restores the compiled-in busy-retry defaults.
func ResetBusyRetryPolicyForTest() {
	busyRetryMu.Lock()
	busyRetryMaxRetries = defaultBusyMaxRetries
	busyRetryInitialBackoff = defaultBusyInitialBackoff
	busyRetryMu.Unlock()
}

// BusyRetryPolicyForTest exposes the active busy-retry policy.
func BusyRetryPolicyForTest() (int, time.Duration) {
	return busyRetryPolicy()
}
