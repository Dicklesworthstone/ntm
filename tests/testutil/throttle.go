package testutil

import (
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

// TmuxTestThrottle limits concurrent tmux session spawning in tests.
// This prevents fork bombs when running tests with high parallelism.
//
// The default limit is 8 concurrent tmux-spawning tests, which is safe
// even on systems with lower process limits. Override with NTM_TEST_PARALLEL.
var TmuxTestThrottle = newThrottle(getTmuxTestLimit())

func getTmuxTestLimit() int {
	if env := os.Getenv("NTM_TEST_PARALLEL"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			return n
		}
	}
	// Default to 8, or GOMAXPROCS/8 if that's larger, capped at 16
	limit := runtime.GOMAXPROCS(0) / 8
	if limit < 8 {
		limit = 8
	}
	if limit > 16 {
		limit = 16
	}
	return limit
}

// throttle is a counting semaphore for limiting concurrent operations.
type throttle struct {
	sem chan struct{}
	mu  sync.Mutex
}

func newThrottle(limit int) *throttle {
	return &throttle{
		sem: make(chan struct{}, limit),
	}
}

// Acquire acquires a slot from the throttle, blocking if necessary.
// Returns a release function that must be called when done.
func (th *throttle) Acquire() func() {
	th.sem <- struct{}{}
	return func() {
		<-th.sem
	}
}

// AcquireForTest acquires a slot and registers cleanup to release it.
// This is the recommended way to use the throttle in tests.
func (th *throttle) AcquireForTest(t *testing.T) {
	t.Helper()
	th.sem <- struct{}{}
	t.Cleanup(func() {
		<-th.sem
	})
}

// RequireTmuxThrottled combines RequireTmux with throttle acquisition.
// Use this at the start of any test that spawns tmux sessions.
//
// Example:
//
//	func TestSpawnSession(t *testing.T) {
//	    testutil.RequireTmuxThrottled(t)
//	    // ... test code that spawns tmux sessions
//	}
func RequireTmuxThrottled(t *testing.T) {
	t.Helper()
	RequireTmux(t)
	TmuxTestThrottle.AcquireForTest(t)
}

// IntegrationTestPrecheckThrottled runs integration prechecks with throttling.
// Use this instead of IntegrationTestPrecheck for tests that spawn tmux.
func IntegrationTestPrecheckThrottled(t *testing.T) {
	t.Helper()
	RequireIntegration(t)
	RequireTmuxThrottled(t)
	RequireNTMBinary(t)
}

// E2ETestPrecheckThrottled runs E2E prechecks with throttling.
// Use this instead of E2ETestPrecheck for tests that spawn tmux.
func E2ETestPrecheckThrottled(t *testing.T) {
	t.Helper()
	RequireE2E(t)
	RequireTmuxThrottled(t)
	RequireNTMBinary(t)
}
