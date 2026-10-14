package cli

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// startDashboardReservationWatcher binds the same target-project configuration
// as the dashboard. Missing configuration, scope or lifecycle cannot authorize
// network access. All values are captured before the optional service wait.
func startDashboardReservationWatcher(parent context.Context, session, projectDir string, effectiveCfg *config.Config) func() {
	if parent == nil || parent.Err() != nil || strings.TrimSpace(session) == "" || strings.TrimSpace(projectDir) == "" ||
		effectiveCfg == nil || !effectiveCfg.FileReservation.Enabled || !effectiveCfg.AgentMail.Enabled {
		return nil
	}

	// Use the canonical client options and token hydration with the TARGET
	// project's settings. The global CLI helper reads launch-directory cfg;
	// calling it here would silently select the wrong endpoint or credential.
	// ConfigOptions preserves the existing environment-over-config precedence.
	opts := []agentmail.Option{agentmail.WithProjectKey(projectDir)}
	// NewClient also has process-wide defaults. Clear those before applying
	// the target configuration: an intentionally empty target token must not
	// inherit another project's credential. Environment and the client's
	// loopback-only Agent Mail setup fallback retain their existing precedence.
	if os.Getenv("AGENT_MAIL_URL") == "" {
		opts = append(opts, agentmail.WithBaseURL(agentmail.DefaultBaseURL))
	}
	if os.Getenv("AGENT_MAIL_TOKEN") == "" {
		opts = append(opts, agentmail.WithToken(""))
	}
	opts = append(opts, agentmail.ConfigOptions(effectiveCfg.AgentMail.URL, effectiveCfg.AgentMail.Token)...)
	amClient := agentmail.NewClient(opts...)
	agentmail.HydrateClientTokensForProject(amClient, projectDir)
	cfgValues := watcher.FileReservationConfigValues{
		Enabled:               effectiveCfg.FileReservation.Enabled,
		AutoReserve:           effectiveCfg.FileReservation.AutoReserve,
		AutoReleaseIdleMin:    effectiveCfg.FileReservation.AutoReleaseIdleMin,
		NotifyOnConflict:      effectiveCfg.FileReservation.NotifyOnConflict,
		ExtendOnActivity:      effectiveCfg.FileReservation.ExtendOnActivity,
		DefaultTTLMin:         effectiveCfg.FileReservation.DefaultTTLMin,
		PollIntervalSec:       effectiveCfg.FileReservation.PollIntervalSec,
		CaptureLinesForDetect: effectiveCfg.FileReservation.CaptureLinesForDetect,
		Debug:                 effectiveCfg.FileReservation.Debug,
	}
	watcherCtx, cancelWatcher := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		startup := dashboardReservationStartup{
			probe: func(ctx context.Context) error {
				_, err := amClient.HealthCheck(ctx)
				return err
			},
		}
		conflictCallback := func(conflict watcher.FileConflict) {
			if cfgValues.Debug {
				log.Printf("[FileReservation] Conflict: %s requested by %s, held by %v",
					conflict.Path, conflict.RequestorAgent, conflict.Holders)
			}
			// The configured watcher publishes to the event bus independently
			// of this optional debug callback.
		}
		startup.run(watcherCtx, func(ctx context.Context) func() {
			reservationWatcher := watcher.NewFileReservationWatcherFromConfig(
				cfgValues, amClient, projectDir,
				"", // Resolve each pane's registered identity; never invent one.
				session, conflictCallback,
			)
			if reservationWatcher == nil || ctx.Err() != nil {
				return nil
			}
			reservationWatcher.Start(ctx)
			if cfgValues.Debug {
				log.Printf("[FileReservation] Watcher started for session %s", session)
			}
			return reservationWatcher.Stop
		})
	}()
	return func() {
		cancelWatcher()
		<-done
	}
}

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
