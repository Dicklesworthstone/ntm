// Package evidencebudget verifies that NTM's interactive operator
// surfaces (robot status, robot snapshot, causality inspection,
// queue-dry diagnostics, dashboard-friendly summaries) return
// promptly with degraded evidence rather than blocking on any one
// provider.
//
// The package composes the per-surface evidence pipeline as a
// declarative Surface{Providers, Compose, Budget} record so tests
// can exercise every degraded-provider permutation through a single
// Evaluate entry point. Providers are simulated via the
// internal/faultharness package, so tests run with no network and
// no real wall-clock waits.
//
// See bd-3v1gs.9.
package evidencebudget

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/faultharness"
)

// SourceHealth classifies a provider's behavior on one Evaluate call.
// The strings are stable so JSON envelopes can pass them through to
// dashboards without reformatting.
type SourceHealth string

const (
	HealthHealthy     SourceHealth = "healthy"
	HealthSlow        SourceHealth = "slow"
	HealthDegraded    SourceHealth = "degraded"
	HealthUnavailable SourceHealth = "unavailable"
	HealthStale       SourceHealth = "stale"
	HealthPartial     SourceHealth = "partial"
)

// Provider names a single coordination data source the surface needs.
// Fetch returns a faultharness.Result so tests can drive every
// failure mode without reaching for the real adapter.
type Provider struct {
	// Name is the canonical short identifier ("mail", "bv", "cass",
	// "tmux", ...). Used in warnings and source_health entries so
	// downstream consumers can route on it.
	Name string

	// Required marks a provider whose Unavailable result should
	// abort Compose. Most providers should be optional so the
	// surface degrades gracefully; tests use Required=true to verify
	// the abort path explicitly.
	Required bool

	// Fetch returns the simulated provider response. Tests inject a
	// closure over a Behavior + Clock; production code would call
	// the real adapter.
	Fetch func(ctx context.Context) faultharness.Result
}

// Surface declares one operator-visible JSON surface and its evidence
// budget. Compose receives the per-provider Results keyed by Provider
// Name and emits the surface's envelope.
type Surface struct {
	Name string

	// Budget is the maximum simulated time the surface may spend
	// gathering evidence. Compose runs after the budget elapses;
	// providers that finish early are recorded with their actual
	// latency. A surface that exceeds Budget is flagged via
	// Evaluation.OverBudget but Compose still runs so the operator
	// still sees something.
	Budget time.Duration

	Providers []Provider

	// Compose builds the surface's envelope from the per-provider
	// results. It must return a JSON-marshallable envelope; the
	// caller wraps the response in an Evaluation that adds source_health
	// + warnings.
	Compose func(results map[string]faultharness.Result) (envelope map[string]any, err error)
}

// Evaluation is the result of running one Surface through the harness.
type Evaluation struct {
	SurfaceName  string                  `json:"surface"`
	Elapsed      time.Duration           `json:"elapsed"`
	Envelope     map[string]any          `json:"envelope"`
	Warnings     []string                `json:"warnings,omitempty"`
	SourceHealth map[string]SourceHealth `json:"source_health"`
	OverBudget   bool                    `json:"over_budget"`
	Err          error                   `json:"-"`
	ErrText      string                  `json:"error,omitempty"`
}

// Evaluate runs every provider serially under clock, then calls
// Compose with the gathered results. Required providers that returned
// Unavailable cause Compose to be skipped and Err to be set.
//
// Evaluate measures elapsed time off the FakeClock, not the real
// wall-clock, so a test can assert "the surface finished in 5s of
// simulated time" without burning that budget for real.
func Evaluate(ctx context.Context, clock faultharness.Clock, surface Surface) Evaluation {
	if clock == nil {
		clock = faultharness.RealClock{}
	}
	start := clock.Now()
	results := make(map[string]faultharness.Result, len(surface.Providers))
	health := make(map[string]SourceHealth, len(surface.Providers))
	var warnings []string

	for _, p := range surface.Providers {
		if p.Fetch == nil {
			continue
		}
		r := p.Fetch(ctx)
		results[p.Name] = r
		h := classifyHealth(r)
		health[p.Name] = h
		for _, w := range r.Warnings {
			warnings = append(warnings, p.Name+": "+w)
		}
	}

	eval := Evaluation{
		SurfaceName:  surface.Name,
		Elapsed:      clock.Now().Sub(start),
		SourceHealth: health,
		Warnings:     uniqueSorted(warnings),
	}
	eval.OverBudget = surface.Budget > 0 && eval.Elapsed > surface.Budget

	for _, p := range surface.Providers {
		if !p.Required {
			continue
		}
		if h := health[p.Name]; h == HealthUnavailable {
			eval.Err = errProvider(p.Name, "required provider unavailable")
			eval.ErrText = eval.Err.Error()
			return eval
		}
	}

	if surface.Compose != nil {
		envelope, err := surface.Compose(results)
		if err != nil {
			eval.Err = err
			eval.ErrText = err.Error()
			return eval
		}
		eval.Envelope = envelope
	}
	return eval
}

func classifyHealth(r faultharness.Result) SourceHealth {
	switch {
	case r.Err == nil && !r.Stale && len(r.Warnings) == 0:
		return HealthHealthy
	case r.Err != nil && (errIs(r.Err, faultharness.ErrUnavailable) || errIs(r.Err, context.DeadlineExceeded)):
		return HealthUnavailable
	case r.Err != nil && errIs(r.Err, faultharness.ErrPartialSuccess):
		return HealthPartial
	case r.Err != nil && errIs(r.Err, faultharness.ErrMalformedJSON):
		return HealthDegraded
	case r.Stale:
		return HealthStale
	case len(r.Warnings) > 0:
		return HealthSlow
	default:
		return HealthHealthy
	}
}

// errIs is a tiny wrapper so this package doesn't pull in errors
// just for one comparison.
func errIs(err, target error) bool {
	if err == nil || target == nil {
		return false
	}
	for e := err; e != nil; {
		if e == target {
			return true
		}
		// Avoid importing errors.Unwrap by using the Unwrap interface.
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// errProvider creates a small synthetic error tagged with the
// provider name so test assertions can match on it.
type providerErr struct {
	provider string
	msg      string
}

func (e *providerErr) Error() string { return e.provider + ": " + e.msg }

func errProvider(name, msg string) error { return &providerErr{provider: name, msg: msg} }

// MarshalJSON implements json.Marshaler so an Evaluation can be
// emitted as a robot-style envelope with deterministic field order.
func (e Evaluation) MarshalJSON() ([]byte, error) {
	type wire struct {
		Surface      string                  `json:"surface"`
		Elapsed      string                  `json:"elapsed"`
		ElapsedMs    int64                   `json:"elapsed_ms"`
		Envelope     map[string]any          `json:"envelope,omitempty"`
		Warnings     []string                `json:"warnings,omitempty"`
		SourceHealth map[string]SourceHealth `json:"source_health"`
		OverBudget   bool                    `json:"over_budget"`
		ErrText      string                  `json:"error,omitempty"`
	}
	return json.Marshal(wire{
		Surface:      e.SurfaceName,
		Elapsed:      e.Elapsed.String(),
		ElapsedMs:    e.Elapsed.Milliseconds(),
		Envelope:     e.Envelope,
		Warnings:     e.Warnings,
		SourceHealth: e.SourceHealth,
		OverBudget:   e.OverBudget,
		ErrText:      e.ErrText,
	})
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
