// Package reservationsim is a deterministic in-memory simulator for
// Agent Mail file reservations. It models acquire / release / expire
// over a virtual clock so tests can drive overlapping glob
// reservations, expired-lease release-on-acquire, and the robot-
// shaped diagnostics that operator surfaces emit when a request
// blocks — all without a live mcp-agent-mail server.
//
// See bd-fxj4f.12.
package reservationsim

import (
	"time"

	"github.com/Dicklesworthstone/ntm/internal/reservationpath"
)

// Outcome is the documented result token for a single Acquire call.
// Stable strings; consumers may route on them.
type Outcome string

const (
	// OutcomeAcquired: the reservation was granted (possibly after
	// reaping an expired lease).
	OutcomeAcquired Outcome = "acquired"
	// OutcomeExpiredReclaimed: the requested pattern was previously
	// held but the prior holder's lease had expired before this
	// call; the simulator reaped the dead lease and granted the new
	// one. Semantically identical to "acquired" but distinguished so
	// tests can verify the reaper actually fired.
	OutcomeExpiredReclaimed Outcome = "expired_reclaimed"
	// OutcomeConflict: a live, exclusive reservation overlaps the
	// requested pattern. The simulator returns the conflicting
	// holder so callers can render diagnostics.
	OutcomeConflict Outcome = "conflict"
	// OutcomeShared: a non-exclusive reservation already covers the
	// pattern, and the request is also non-exclusive. Both holders
	// coexist.
	OutcomeShared Outcome = "shared"
	// OutcomeInvalid: the request itself was malformed (empty
	// pattern, empty agent, or non-positive TTL).
	OutcomeInvalid Outcome = "invalid"
)

// Lease is one in-memory reservation. Pattern can be exact ("foo.go"),
// trailing glob ("internal/auth/**", "internal/auth/*"), or any
// pattern path.Match accepts. AgentName attributes the holder.
type Lease struct {
	ID          int
	PathPattern string
	AgentName   string
	Exclusive   bool
	AcquiredAt  time.Time
	ExpiresAt   time.Time
	Reason      string
}

// AcquireRequest configures one Acquire call.
type AcquireRequest struct {
	PathPattern string
	AgentName   string
	Exclusive   bool
	TTL         time.Duration
	Reason      string
}

// AcquireResult captures the outcome plus the conflicting lease (if
// any). Diagnostic is the robot-shaped string surfaces should render.
type AcquireResult struct {
	Outcome    Outcome `json:"outcome"`
	Lease      *Lease  `json:"lease,omitempty"`
	Conflict   *Lease  `json:"conflict,omitempty"`
	Diagnostic string  `json:"diagnostic,omitempty"`
}

// Simulator is the mutable model. Use NewSimulator and pass a Clock
// for tests; production callers can supply RealClock.
type Simulator struct {
	clock  Clock
	leases []*Lease
	nextID int
}

// Clock abstracts time.Now so tests can advance reservations without
// burning real wall-clock.
type Clock interface {
	Now() time.Time
}

// fixedClock is a small Clock used by tests.
type fixedClock struct{ now time.Time }

// patternsOverlap uses the same bounded path-language intersection as live
// Agent Mail and coordinator conflict checks. Matching one glob's spelling
// against another misses crossing scopes, basename globs and literal subtrees.
// Invalid or overly complex patterns cannot establish disjointness, so advice
// conservatively surfaces them as potential conflicts. This is read-only risk
// evidence, never authorization to release another holder's reservation.
func patternsOverlap(a, b string) bool {
	// Preserve the advisor's historical repository-wide /** alias. Apart
	// from this alias, pass patterns verbatim: whitespace belongs to paths.
	if a == "/**" {
		a = "**"
	}
	if b == "/**" {
		b = "**"
	}
	return reservationpath.MayOverlap(a, b)
}
