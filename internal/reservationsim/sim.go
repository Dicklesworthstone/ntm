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
	"path"
	"strings"
	"time"
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

// patternsOverlap returns true when the two reservation patterns
// could ever cover a common path. Supports exact match, trailing
// "/**" deep glob, trailing "/*" one-segment glob, and falls back
// to path.Match for generic globs. Two patterns overlap iff one of
// these holds:
//   - they are equal,
//   - one is a /** prefix of the other,
//   - one is a /* of a one-segment match,
//   - the simpler pattern matches the more-specific path,
//   - generic glob match in either direction.
func patternsOverlap(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if patternCovers(a, b) || patternCovers(b, a) {
		return true
	}
	return false
}

// patternCovers reports whether pattern p could match a path that
// the more-specific pattern q targets.
func patternCovers(p, q string) bool {
	if p == q {
		return true
	}
	// bd-6286k: bare "**" is a catch-all just like "/**". Without this,
	// strings.HasSuffix("**", "/**") returns false (the pattern is
	// shorter than the suffix), and path.Match("**", "foo/bar.go")
	// returns false too (path.Match's `*` cannot cross `/`), so plain
	// `**` would silently fail to overlap deep paths.
	if p == "**" {
		return true
	}
	if strings.HasSuffix(p, "/**") {
		prefix := strings.TrimSuffix(p, "/**")
		if prefix == "" {
			return true // p == "/**"
		}
		// Strip any trailing wildcard from q so we compare prefixes.
		qp := strings.TrimSuffix(q, "/**")
		qp = strings.TrimSuffix(qp, "/*")
		return qp == prefix || strings.HasPrefix(qp, prefix+"/")
	}
	if strings.HasSuffix(p, "/*") {
		prefix := strings.TrimSuffix(p, "/*")
		// q must be a single-segment child of prefix.
		if !strings.HasPrefix(q, prefix+"/") {
			return false
		}
		rest := strings.TrimPrefix(q, prefix+"/")
		// p covers q iff q is an exact one-segment match. /** matches
		// here because /** ⊃ /*; covered by the prefix-equality branch.
		return rest != "" && !strings.Contains(rest, "/")
	}
	if matched, err := path.Match(p, q); err == nil && matched {
		return true
	}
	return false
}
