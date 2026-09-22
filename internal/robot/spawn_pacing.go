package robot

import (
	"context"
	"errors"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// WithSpawnLaunchInterval adds a minimum start-to-start interval to one spawn
// request. It wraps the existing launcher, so topology validation, admission,
// readiness, assignment, and partial-failure reporting still belong to GetSpawn.
// The first launch is immediate. A slow or failed launch consumes its interval;
// there is no catch-up burst and no wait after the final launch. Dry runs never
// call the launcher and therefore never wait.
//
// Call this once per request. The returned options own their pacing state and
// a copy of the lifecycle ports; the caller's options and ports are not mutated.
// A zero interval preserves the original options without installing a wrapper.
func WithSpawnLaunchInterval(opts SpawnOptions, interval time.Duration) (SpawnOptions, error) {
	if interval < 0 {
		return opts, errors.New("launch_interval must be a non-negative duration")
	}
	if interval == 0 {
		return opts, nil
	}

	var deps SpawnLifecycleDependencies
	if opts.LifecycleDeps != nil {
		deps = *opts.LifecycleDeps
	}
	launch := deps.LaunchAgent
	if launch == nil {
		launch = launchAgent
	}

	// GetSpawn launches sequentially. Keep the policy safe even when a caller
	// invokes its lifecycle port concurrently, without an uncancellable mutex
	// wait or reserving future slots for requests that have already cancelled.
	gate := make(chan struct{}, 1)
	var nextStart time.Time
	deps.LaunchAgent = func(ctx context.Context, pane tmux.Pane, session, agentType string, number int, dir, command string) (SpawnedAgent, error) {
		if ctx == nil {
			return SpawnedAgent{}, errors.New("paced spawn requires a context")
		}
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return SpawnedAgent{}, ctx.Err()
		}
		defer func() { <-gate }()
		if err := ctx.Err(); err != nil {
			return SpawnedAgent{}, err
		}
		if delay := time.Until(nextStart); delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return SpawnedAgent{}, ctx.Err()
			}
		}
		// Cancellation and the timer can become ready together. Do not turn
		// that race into an extra launch after the operator cancels the job.
		if err := ctx.Err(); err != nil {
			return SpawnedAgent{}, err
		}
		nextStart = time.Now().Add(interval)
		return launch(ctx, pane, session, agentType, number, dir, command)
	}
	opts.LifecycleDeps = &deps
	return opts, nil
}
