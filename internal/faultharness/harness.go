// Package faultharness provides a small, deterministic test harness
// for simulating slow, unavailable, malformed, stale, and partial
// responses from the four coordination providers NTM depends on:
// Agent Mail, bv (graph triage), CASS (cross-agent search), and
// tmux (pane capture).
//
// The harness is Go-only and has no network dependency. It uses a
// pluggable Clock so tests advance simulated time without real
// sleeps; the FakeClock implementation records each Sleep call so a
// test can assert "the caller waited for N seconds" without burning
// wall-clock budget.
//
// See bd-3v1gs.8.
package faultharness

import (
	"context"
	"errors"
	"sync"
	"time"
)

// FailureMode enumerates the degraded-source shapes the harness can
// inject. ModeHealthy is the no-op baseline.
type FailureMode string

const (
	// ModeHealthy returns the caller-supplied healthy payload after
	// Behavior.Latency has elapsed (typically zero).
	ModeHealthy FailureMode = "healthy"

	// ModeSlow waits Behavior.Latency, then returns the healthy
	// payload. The caller observes the latency via Result.Latency.
	ModeSlow FailureMode = "slow"

	// ModeDeadlineExceeded waits up to Behavior.Latency or the
	// caller's ctx deadline, whichever fires first, then returns
	// context.DeadlineExceeded. If ctx has no deadline the harness
	// still returns the deadline error after Latency, so tests can
	// drive the timeout path without configuring ctx.
	ModeDeadlineExceeded FailureMode = "deadline_exceeded"

	// ModeUnavailable returns immediately with ErrUnavailable.
	ModeUnavailable FailureMode = "unavailable"

	// ModeMalformedJSON returns Behavior.Payload (which the caller
	// has populated with junk bytes) and a nil error — matching what
	// a mis-coded provider would do.
	ModeMalformedJSON FailureMode = "malformed_json"

	// ModeStaleCache returns the healthy payload but Result.Stale is
	// true and Result.StaleAge is set, so the caller can decide
	// whether to surface a "stale data" warning.
	ModeStaleCache FailureMode = "stale_cache"

	// ModePartialSuccess returns Behavior.Payload (the partial body
	// the caller pre-built) along with a nil error and a Warning so
	// the caller can flag "data incomplete".
	ModePartialSuccess FailureMode = "partial_success"
)

// Sentinel errors. Tests use errors.Is to assert the failure path
// without coupling to error-message wording.
var (
	ErrUnavailable    = errors.New("provider unavailable")
	ErrMalformedJSON  = errors.New("malformed json payload")
	ErrPartialSuccess = errors.New("partial success: result truncated")
)

// Behavior configures one Apply call. Callers populate the fields
// that match their chosen Mode and leave the rest zero.
type Behavior struct {
	Mode FailureMode

	// Latency is how long Apply should wait before returning. For
	// ModeSlow it's the success latency; for ModeDeadlineExceeded
	// it's the timeout budget.
	Latency time.Duration

	// Payload is the raw body returned by ModeMalformedJSON and
	// ModePartialSuccess. Ignored by other modes.
	Payload []byte

	// StaleSince marks when the cached data was generated, used by
	// ModeStaleCache to compute Result.StaleAge.
	StaleSince time.Time

	// Warning is an optional human-readable line that ModePartialSuccess
	// and ModeStaleCache surface in Result.Warnings. Empty falls back
	// to a default per Mode.
	Warning string
}

// Result is the harness's response. Latency is what the harness
// actually "spent" on this call (for ModeSlow this equals
// Behavior.Latency; for ModeUnavailable it's zero; for
// ModeDeadlineExceeded it's the budget consumed before the deadline).
type Result struct {
	Latency  time.Duration
	Payload  []byte
	Err      error
	Stale    bool
	StaleAge time.Duration
	Warnings []string
}

// RecoveryAction is the operator action a drill expects the caller to surface.
type RecoveryAction string

const (
	RecoveryActionNone                  RecoveryAction = "none"
	RecoveryActionRetryWithBackoff      RecoveryAction = "retry_with_backoff"
	RecoveryActionRefreshBeforeMutation RecoveryAction = "refresh_before_mutation"
	RecoveryActionRequestFreshSnapshot  RecoveryAction = "request_fresh_snapshot"
	RecoveryActionSurfacePartialResult  RecoveryAction = "surface_partial_result"
	RecoveryActionResumeFromCheckpoint  RecoveryAction = "resume_from_checkpoint"
	RecoveryActionEscalate              RecoveryAction = "escalate"
)

// ProviderFunc is a fake provider invoked by a recovery drill step.
type ProviderFunc func(context.Context, Clock, Behavior) Result

// RecoveryDrill is a deterministic scenario made of provider failure steps.
type RecoveryDrill struct {
	Name  string
	Steps []RecoveryDrillStep
}

// RecoveryDrillStep describes one simulated failure or healthy provider call.
type RecoveryDrillStep struct {
	Name     string
	Provider string
	Behavior Behavior
	Run      ProviderFunc
}

// RecoveryDrillReport is the scenario output tests can assert against.
type RecoveryDrillReport struct {
	Name     string
	Outcomes []RecoveryDrillOutcome
}

// RecoveryDrillOutcome records the user-visible result for one step.
type RecoveryDrillOutcome struct {
	Name           string
	Provider       string
	Mode           FailureMode
	Latency        time.Duration
	StaleAge       time.Duration
	Warnings       []string
	Err            error
	Diagnostic     string
	RecoveryAction RecoveryAction
}

// Clock abstracts time.Now and time.Sleep so tests can advance
// simulated time without burning real wall-clock.
type Clock interface {
	Now() time.Time
	// Sleep blocks for d, returning ctx.Err() if ctx fires first.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the production Clock backed by time.Now and a
// timer-driven sleep that respects ctx.
type RealClock struct{}

// Now returns the current real time.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep blocks for d, returning ctx.Err() if ctx fires first.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FakeClock records every Sleep call without actually waiting. Tests
// inspect Sleeps to assert the harness behaved as expected. NowTime
// can be advanced by the test directly between Apply calls.
type FakeClock struct {
	mu      sync.Mutex
	NowTime time.Time
	Sleeps  []time.Duration
}

// Now returns NowTime. Concurrent reads are safe via a small mutex.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.NowTime
}

// Sleep records d and advances NowTime by d. Returns ctx.Err() when
// ctx is already cancelled at call time. Zero-or-negative durations
// are no-ops (matching time.Sleep semantics) and are NOT recorded —
// callers asserting "did the harness pause?" should not see a noise
// entry for d=0.
func (c *FakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	c.mu.Lock()
	c.Sleeps = append(c.Sleeps, d)
	c.NowTime = c.NowTime.Add(d)
	c.mu.Unlock()
	return nil
}

// Apply runs Behavior under clock and returns the simulated Result.
// healthyPayload is the body to return for the success modes; it is
// ignored when Behavior.Payload supersedes it (malformed/partial).
func Apply(ctx context.Context, clock Clock, b Behavior, healthyPayload []byte) Result {
	if clock == nil {
		clock = RealClock{}
	}
	switch b.Mode {
	case ModeHealthy:
		_ = clock.Sleep(ctx, b.Latency)
		return Result{Latency: b.Latency, Payload: healthyPayload}

	case ModeSlow:
		_ = clock.Sleep(ctx, b.Latency)
		return Result{
			Latency: b.Latency,
			Payload: healthyPayload,
			Warnings: []string{
				warningOrDefault(b.Warning, "slow_response"),
			},
		}

	case ModeDeadlineExceeded:
		// Sleep up to Latency or until ctx fires. Either way, the
		// caller observes a deadline-exceeded error. Result.Latency
		// reports the ACTUAL elapsed time (via clock.Now() bracketing
		// the Sleep call) rather than the requested budget — so a
		// caller whose ctx fires after 100ms while Latency was 5s
		// sees Latency≈100ms in the result, not 5s. Pre-bd-otcf0 the
		// branch always reported b.Latency regardless of how Sleep
		// returned, masking ctx-cancel paths under RealClock.
		before := clock.Now()
		_ = clock.Sleep(ctx, b.Latency)
		spent := clock.Now().Sub(before)
		// Defensive cap: a clock that ran backwards or was advanced
		// past the requested budget would otherwise emit a misleading
		// spent. The harness contract is "the harness spent this
		// long" — at most b.Latency, never less than zero.
		if spent < 0 {
			spent = 0
		}
		if spent > b.Latency {
			spent = b.Latency
		}
		return Result{
			Latency: spent,
			Err:     context.DeadlineExceeded,
			Warnings: []string{
				warningOrDefault(b.Warning, "deadline_exceeded"),
			},
		}

	case ModeUnavailable:
		return Result{
			Err: ErrUnavailable,
			Warnings: []string{
				warningOrDefault(b.Warning, "provider_unavailable"),
			},
		}

	case ModeMalformedJSON:
		payload := b.Payload
		if payload == nil {
			payload = []byte(`{"unterminated": `)
		}
		return Result{
			Payload: payload,
			Err:     ErrMalformedJSON,
			Warnings: []string{
				warningOrDefault(b.Warning, "malformed_json"),
			},
		}

	case ModeStaleCache:
		stale := time.Time{}
		var age time.Duration
		if !b.StaleSince.IsZero() {
			stale = b.StaleSince
			age = clock.Now().Sub(stale)
			if age < 0 {
				age = 0
			}
		}
		return Result{
			Payload:  healthyPayload,
			Stale:    true,
			StaleAge: age,
			Warnings: []string{
				warningOrDefault(b.Warning, "stale_cache"),
			},
		}

	case ModePartialSuccess:
		payload := b.Payload
		if payload == nil {
			payload = healthyPayload
		}
		return Result{
			Payload: payload,
			Err:     ErrPartialSuccess,
			Warnings: []string{
				warningOrDefault(b.Warning, "partial_success"),
			},
		}
	}
	// Unknown mode — surface as Unavailable so callers don't silently
	// accept a typo.
	return Result{
		Err: ErrUnavailable,
		Warnings: []string{
			"unknown_failure_mode:" + string(b.Mode),
		},
	}
}

// RunRecoveryDrill executes each scenario step and classifies the
// operator-facing diagnostic and recovery action. It is intentionally
// provider-agnostic: tests can pass the built-in fakes or a custom step
// function for package-specific failures such as interrupted pipelines.
func RunRecoveryDrill(ctx context.Context, clock Clock, drill RecoveryDrill) RecoveryDrillReport {
	if clock == nil {
		clock = RealClock{}
	}
	report := RecoveryDrillReport{
		Name:     drill.Name,
		Outcomes: make([]RecoveryDrillOutcome, 0, len(drill.Steps)),
	}
	for _, step := range drill.Steps {
		run := step.Run
		if run == nil {
			run = func(context.Context, Clock, Behavior) Result {
				return Result{
					Err: ErrUnavailable,
					Warnings: []string{
						"missing_provider",
					},
				}
			}
		}
		result := run(ctx, clock, step.Behavior)
		diagnostic, action := classifyRecovery(step.Provider, result)
		report.Outcomes = append(report.Outcomes, RecoveryDrillOutcome{
			Name:           step.Name,
			Provider:       step.Provider,
			Mode:           step.Behavior.Mode,
			Latency:        result.Latency,
			StaleAge:       result.StaleAge,
			Warnings:       append([]string(nil), result.Warnings...),
			Err:            result.Err,
			Diagnostic:     diagnostic,
			RecoveryAction: action,
		})
	}
	return report
}

func classifyRecovery(provider string, r Result) (string, RecoveryAction) {
	if provider == "" {
		provider = "provider"
	}
	switch {
	case errors.Is(r.Err, context.Canceled):
		return provider + " interrupted", RecoveryActionResumeFromCheckpoint
	case errors.Is(r.Err, context.DeadlineExceeded):
		return provider + " timed out", RecoveryActionRetryWithBackoff
	case errors.Is(r.Err, ErrUnavailable):
		return provider + " unavailable", RecoveryActionRetryWithBackoff
	case errors.Is(r.Err, ErrMalformedJSON):
		return provider + " returned malformed output", RecoveryActionRequestFreshSnapshot
	case errors.Is(r.Err, ErrPartialSuccess):
		return provider + " returned partial result", RecoveryActionSurfacePartialResult
	case r.Stale:
		return provider + " returned stale data", RecoveryActionRefreshBeforeMutation
	case r.Err != nil:
		return provider + " failed: " + r.Err.Error(), RecoveryActionEscalate
	case len(r.Warnings) > 0:
		return provider + " warning: " + r.Warnings[0], RecoveryActionEscalate
	default:
		return provider + " healthy", RecoveryActionNone
	}
}

func warningOrDefault(custom, fallback string) string {
	if custom != "" {
		return custom
	}
	return fallback
}
