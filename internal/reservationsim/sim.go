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
	"sort"
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

func (f *fixedClock) Now() time.Time { return f.now }

// NewSimulator returns an empty simulator wired to clk. clk == nil
// uses time.Now via realClock{}.
func NewSimulator(clk Clock) *Simulator {
	if clk == nil {
		clk = realClock{}
	}
	return &Simulator{clock: clk}
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Acquire attempts to grant the requested reservation. The simulator
// reaps any leases whose ExpiresAt has passed before evaluating.
func (s *Simulator) Acquire(req AcquireRequest) AcquireResult {
	pat := strings.TrimSpace(req.PathPattern)
	agent := strings.TrimSpace(req.AgentName)
	if pat == "" || agent == "" || req.TTL <= 0 {
		return AcquireResult{
			Outcome:    OutcomeInvalid,
			Diagnostic: "invalid request: PathPattern, AgentName, and positive TTL are required",
		}
	}

	now := s.clock.Now()
	// Pass the requested pattern so the reaper can report whether any
	// of the leases it just removed actually overlapped it. The
	// OutcomeExpiredReclaimed contract is "the requested pattern was
	// previously held" — a reap of an unrelated stale lease must NOT
	// promote a fresh acquire to that outcome (bd-zshpx).
	reapedOverlap := s.reapExpiredLocked(now, pat)

	// Look for live conflicts.
	for _, l := range s.leases {
		if !patternsOverlap(l.PathPattern, pat) {
			continue
		}
		if !req.Exclusive && !l.Exclusive {
			continue // both shared, both coexist
		}
		// Conflict: at least one side wants exclusivity AND patterns overlap.
		conflict := *l
		return AcquireResult{
			Outcome:  OutcomeConflict,
			Conflict: &conflict,
			Diagnostic: "reservation_conflict: pattern=" + pat + " requested_by=" + agent +
				" held_by=" + l.AgentName + " held_pattern=" + l.PathPattern,
		}
	}

	// No live conflict — grant.
	s.nextID++
	lease := &Lease{
		ID:          s.nextID,
		PathPattern: pat,
		AgentName:   agent,
		Exclusive:   req.Exclusive,
		AcquiredAt:  now,
		ExpiresAt:   now.Add(req.TTL),
		Reason:      strings.TrimSpace(req.Reason),
	}
	s.leases = append(s.leases, lease)
	out := OutcomeAcquired
	if reapedOverlap {
		out = OutcomeExpiredReclaimed
	}
	leaseCopy := *lease
	other := s.otherSharedHolder(lease)
	if out == OutcomeAcquired && !req.Exclusive && other != nil {
		out = OutcomeShared
		oc := *other
		return AcquireResult{Outcome: out, Lease: &leaseCopy, Conflict: &oc}
	}
	return AcquireResult{Outcome: out, Lease: &leaseCopy}
}

// Release removes the lease with the given ID. Returns true if it
// existed.
func (s *Simulator) Release(id int) bool {
	for i, l := range s.leases {
		if l.ID == id {
			s.leases = append(s.leases[:i], s.leases[i+1:]...)
			return true
		}
	}
	return false
}

// ReleaseByAgent removes every lease held by the named agent.
// Returns the number of leases removed.
func (s *Simulator) ReleaseByAgent(agent string) int {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return 0
	}
	kept := s.leases[:0]
	removed := 0
	for _, l := range s.leases {
		if strings.EqualFold(l.AgentName, agent) {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	s.leases = kept
	return removed
}

// Active returns a snapshot of all currently-held leases. Returned
// slice is a copy; mutating it does not affect the simulator.
func (s *Simulator) Active() []Lease {
	now := s.clock.Now()
	s.reapExpiredLocked(now, "")
	out := make([]Lease, len(s.leases))
	for i, l := range s.leases {
		out[i] = *l
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PathPattern != out[j].PathPattern {
			return out[i].PathPattern < out[j].PathPattern
		}
		return out[i].AgentName < out[j].AgentName
	})
	return out
}

// Diagnostics returns the robot-shaped envelope a `--robot-locks`-
// style surface would render. Stable JSON; deterministic sort.
type DiagnosticsReport struct {
	GeneratedAt   time.Time `json:"generated_at"`
	ActiveCount   int       `json:"active_count"`
	Leases        []Lease   `json:"leases"`
	ConflictHints []string  `json:"conflict_hints,omitempty"`
}

// Diagnostics returns the rendered report for the current state.
// ConflictHints lists every (held, candidate) pair where the held
// lease is exclusive and a candidate request from any other agent
// for the same path would be blocked — useful so a pre-check can
// say "this exact path is currently locked".
func (s *Simulator) Diagnostics() DiagnosticsReport {
	now := s.clock.Now()
	s.reapExpiredLocked(now, "")
	rep := DiagnosticsReport{
		GeneratedAt: now.UTC(),
		Leases:      s.Active(),
	}
	rep.ActiveCount = len(rep.Leases)
	for _, l := range rep.Leases {
		if !l.Exclusive {
			continue
		}
		rep.ConflictHints = append(rep.ConflictHints,
			"exclusive lease on "+l.PathPattern+" held by "+l.AgentName+" until "+l.ExpiresAt.UTC().Format(time.RFC3339))
	}
	sort.Strings(rep.ConflictHints)
	return rep
}

// reapExpiredLocked removes every lease whose ExpiresAt is before now.
//
// pat scopes the returned overlap boolean to a candidate pattern: when
// non-empty, the bool is true only if at least one of the reaped
// leases overlapped pat (per patternsOverlap). Acquire passes the
// requesting pattern so its OutcomeExpiredReclaimed signal honors the
// "the requested pattern was previously held" contract (bd-zshpx).
// Active / Diagnostics pass "" because they only need the side effect.
func (s *Simulator) reapExpiredLocked(now time.Time, pat string) bool {
	if len(s.leases) == 0 {
		return false
	}
	pat = strings.TrimSpace(pat)
	kept := s.leases[:0]
	overlap := false
	for _, l := range s.leases {
		if !l.ExpiresAt.IsZero() && !now.Before(l.ExpiresAt) {
			if pat != "" && patternsOverlap(l.PathPattern, pat) {
				overlap = true
			}
			continue
		}
		kept = append(kept, l)
	}
	s.leases = kept
	return overlap
}

// otherSharedHolder returns the first existing shared lease that
// overlaps `lease` and is held by a different agent. Used to mark
// the new lease as OutcomeShared rather than plain OutcomeAcquired.
func (s *Simulator) otherSharedHolder(lease *Lease) *Lease {
	for _, l := range s.leases {
		if l == lease {
			continue
		}
		if l.Exclusive {
			continue
		}
		if !patternsOverlap(l.PathPattern, lease.PathPattern) {
			continue
		}
		if strings.EqualFold(l.AgentName, lease.AgentName) {
			continue
		}
		return l
	}
	return nil
}

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
