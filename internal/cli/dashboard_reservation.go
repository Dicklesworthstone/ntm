package cli

import (
	"context"
	"time"
)

// dashboardReservationStartup waits for the optional Agent Mail dependency
// without parking the TUI or permanently disabling reservations after one
// failed startup probe. The probe is read-only; reservation mutations remain
// in the existing watcher after readiness has been observed.
type dashboardReservationStartup struct {
	probe func(context.Context) error
	// wait is an optional deterministic clock boundary for lifecycle tests.
	// Production always uses the cancellable timer below.
	wait func(context.Context, time.Duration) bool
}

// run owns the single watcher started by the dashboard. Cancellation joins
// its cleanup before the enclosing dashboard stop function returns. A service
// outage may defer startup, but never creates another concurrent watcher.
func (s dashboardReservationStartup) run(ctx context.Context, start func(context.Context) func()) {
	if start == nil || !s.waitUntilReady(ctx) || ctx.Err() != nil {
		return
	}
	stop := start(ctx)
	if stop == nil {
		return
	}
	defer stop()
	<-ctx.Done()
}

func (s dashboardReservationStartup) waitUntilReady(ctx context.Context) bool {
	if ctx == nil || s.probe == nil {
		return false
	}
	wait := s.wait
	if wait == nil {
		wait = waitDashboardReservationRetry
	}
	const maxDelay = 30 * time.Second
	delay := time.Second
	for ctx.Err() == nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.probe(probeCtx)
		// A late success after either deadline is not readiness evidence.
		ready := err == nil && probeCtx.Err() == nil
		cancel()
		if ctx.Err() != nil {
			return false
		}
		if ready {
			return true
		}
		if !wait(ctx, delay) {
			return false
		}
		delay = min(2*delay, maxDelay)
	}
	return false
}

func waitDashboardReservationRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
