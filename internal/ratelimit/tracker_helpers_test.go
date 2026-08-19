package ratelimit

import "time"

// NewCodexThrottle creates a CodexThrottle with the given max concurrency
// ceiling (test-only constructor; production constructs the throttle via
// struct wiring elsewhere).
func NewCodexThrottle(maxConcurrent int) *CodexThrottle {
	if maxConcurrent < 1 {
		maxConcurrent = 3
	}
	return &CodexThrottle{
		phase:             ThrottleNormal,
		allowedConcurrent: maxConcurrent,
		maxConcurrent:     maxConcurrent,
		nowFn:             time.Now,
	}
}
